package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"yyb_go/internal/store"
)

// ---------- v10：登录账号级定时任务 + 脚本目录可见范围 ----------

// newOwnershipQingLongApp 同时启用控制台鉴权与青龙面板：归属隔离和面板任务
// 要一起验证，单靠 newRunsTestApp / newOwnershipApp 都不够。
func newOwnershipQingLongApp(t *testing.T, qlURL string) (http.Handler, *App) {
	t.Helper()
	t.Setenv("GIN_MODE", "test")
	app, err := NewApp(Config{
		ResourceRoot:     t.TempDir(),
		RequestTimeout:   time.Second,
		SessionTTL:       time.Minute,
		QRSessionTTL:     time.Minute,
		QingLongURL:      qlURL,
		QingLongClientID: "client-id",
		QingLongSecret:   "client-secret",
		QingLongServer:   "yyb-go:8000",
		AuthDriver:       "sqlite",
		AllowNoAuth:      true,
	})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app.Handler(), app
}

// seedConsoleUsers 建管理员（首个注册账号）+ 普通用户 alice，返回两人的会话。
func seedConsoleUsers(t *testing.T, handler http.Handler) (adminCookie, aliceCookie *http.Cookie, aliceID int64) {
	t.Helper()
	register := serveJSON(t, handler, http.MethodPost, "/register",
		`{"username":"owner","displayName":"Owner","password":"`+ownershipAdminPassword+`"}`, nil)
	if register.Code != http.StatusCreated {
		t.Fatalf("register = %d %s", register.Code, register.Body.String())
	}
	adminCookie = sessionCookieOf(t, register)
	created := serveJSON(t, handler, http.MethodPost, "/api/auth/users",
		`{"username":"alice","display_name":"Alice","password":"`+ownershipUserPassword+`","role":"user"}`, adminCookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("create alice = %d %s", created.Code, created.Body.String())
	}
	var payload struct {
		ID int64 `json:"id"`
	}
	decodeData(t, created, &payload)
	aliceID = payload.ID
	login := serveJSON(t, handler, http.MethodPost, "/login",
		`{"username":"alice","password":"`+ownershipUserPassword+`"}`, nil)
	aliceCookie = sessionCookieOf(t, login)
	return adminCookie, aliceCookie, aliceID
}

// managedCronsByScope 按日志目录名区分两类托管任务：
// yyb_account_ 前缀 = 只跑一个账号的手动任务；yyb_owner_ 前缀 = 登录账号级定时任务。
func managedCronsByScope(fake *fakeQingLong) (accountCrons, ownerCrons []qingLongCron) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, cron := range fake.crons {
		switch {
		case strings.HasPrefix(cron.LogName, "yyb_owner_"):
			ownerCrons = append(ownerCrons, cron)
		case strings.HasPrefix(cron.LogName, "yyb_account_"):
			accountCrons = append(accountCrons, cron)
		}
	}
	return accountCrons, ownerCrons
}

func refOf(id int64) string { return strconv.FormatInt(id, 10) }

// 定时任务按「网关登录账号」隔离：一个登录账号 + 一个脚本只建一条任务，
// 跑该登录账号名下的全部 code 账号，且绝不带上别的登录账号的账号。
func TestLoginAccountScheduleCoversOwnedAccountsOnly(t *testing.T) {
	fake, server := newFakeQingLong(t)
	handler, app := newOwnershipQingLongApp(t, server.URL)
	adminCookie, aliceCookie, aliceID := seedConsoleUsers(t, handler)

	aliceA := seedAccount(t, app, "oALICE0account0one", &aliceID)
	aliceB := seedAccount(t, app, "oALICE0account0two", &aliceID)
	adminAcc := seedAccount(t, app, "oADMIN0account0one", nil)

	// alice 在账号 A 上开定时
	enable := serveJSON(t, handler, http.MethodPut, "/api/qinglong/jobs/enable",
		`{"ref":"`+refOf(aliceA)+`","script_key":"code脚本/绿鼻子.js","enabled":true}`, aliceCookie)
	if enable.Code != http.StatusOK {
		t.Fatalf("enable = %d %s", enable.Code, enable.Body.String())
	}
	_, ownerCrons := managedCronsByScope(fake)
	if len(ownerCrons) != 1 {
		t.Fatalf("登录账号级任务数 = %d，期望 1（%v）", len(ownerCrons), ownerCrons)
	}
	before := ownerCrons[0].TaskBefore
	if !strings.Contains(before, "oALICE0account0one") || !strings.Contains(before, "oALICE0account0two") {
		t.Fatalf("任务前命令没有覆盖 alice 名下全部账号: %q", before)
	}
	if strings.Contains(before, "oADMIN0account0one") {
		t.Fatalf("任务前命令串到了别的登录账号: %q", before)
	}
	if ownerCrons[0].IsDisabled == nil || *ownerCrons[0].IsDisabled != 0 {
		t.Fatalf("定时任务建出来后没有被启用: %+v", ownerCrons[0])
	}
	if !strings.HasPrefix(ownerCrons[0].Name, "[YYB: u") {
		t.Fatalf("登录账号级任务命名 = %q", ownerCrons[0].Name)
	}

	// 同一个登录账号下的另一个账号看同一个脚本：开关状态是共享的
	listB := serveJSON(t, handler, http.MethodGet, "/api/qinglong/jobs?ref="+refOf(aliceB), "", aliceCookie)
	payloadB := decodeJobsPayload(t, listB.Body.Bytes())
	if payloadB.ScopeCount != 2 {
		t.Fatalf("scope_count = %d，期望 2（alice 名下的 code 账号数）", payloadB.ScopeCount)
	}
	enabledFound := false
	for _, job := range payloadB.Jobs {
		if job.ScriptKey == "code脚本/绿鼻子.js" {
			enabledFound = job.Enabled && job.Provisioned
		}
	}
	if !enabledFound {
		t.Fatalf("同一登录账号的另一个账号没看到已启用的定时任务: %+v", payloadB.Jobs)
	}

	// 管理员名下账号是另一个隔离域，各建各的
	enableAdmin := serveJSON(t, handler, http.MethodPut, "/api/qinglong/jobs/enable",
		`{"ref":"`+refOf(adminAcc)+`","script_key":"code脚本/绿鼻子.js","enabled":true}`, adminCookie)
	if enableAdmin.Code != http.StatusOK {
		t.Fatalf("admin enable = %d %s", enableAdmin.Code, enableAdmin.Body.String())
	}
	_, ownerCrons = managedCronsByScope(fake)
	if len(ownerCrons) != 2 {
		t.Fatalf("登录账号级任务数 = %d，期望 2（两个登录账号各一条）", len(ownerCrons))
	}
	for _, cron := range ownerCrons {
		hasAlice := strings.Contains(cron.TaskBefore, "oALICE0")
		hasAdmin := strings.Contains(cron.TaskBefore, "oADMIN0")
		if hasAlice && hasAdmin {
			t.Fatalf("两个登录账号的账号串到了同一条任务里: %q", cron.TaskBefore)
		}
		if !hasAlice && !hasAdmin {
			t.Fatalf("任务前命令里没有任何账号: %q", cron.TaskBefore)
		}
	}

	// 停用只影响这个登录账号
	disable := serveJSON(t, handler, http.MethodPut, "/api/qinglong/jobs/enable",
		`{"ref":"`+refOf(aliceA)+`","script_key":"code脚本/绿鼻子.js","enabled":false}`, aliceCookie)
	if disable.Code != http.StatusOK {
		t.Fatalf("disable = %d %s", disable.Code, disable.Body.String())
	}
	_, ownerCrons = managedCronsByScope(fake)
	for _, cron := range ownerCrons {
		disabled := cron.IsDisabled != nil && *cron.IsDisabled != 0
		if strings.Contains(cron.TaskBefore, "oALICE0") && !disabled {
			t.Fatalf("alice 的定时任务没有被停用: %+v", cron)
		}
		if strings.Contains(cron.TaskBefore, "oADMIN0") && disabled {
			t.Fatalf("停用 alice 的任务时误停了管理员的: %+v", cron)
		}
	}
}

// 「运行」永远只跑选中的那个 code 账号，与定时的「跑全部」区分开。
func TestManualRunStaysScopedToSingleAccount(t *testing.T) {
	fake, server := newFakeQingLong(t)
	handler, app := newOwnershipQingLongApp(t, server.URL)
	_, aliceCookie, aliceID := seedConsoleUsers(t, handler)

	aliceA := seedAccount(t, app, "oALICE0account0one", &aliceID)
	seedAccount(t, app, "oALICE0account0two", &aliceID)

	run := serveJSON(t, handler, http.MethodPost, "/api/qinglong/jobs/run",
		`{"ref":"`+refOf(aliceA)+`","script_key":"code脚本/绿鼻子.js"}`, aliceCookie)
	if run.Code != http.StatusAccepted {
		t.Fatalf("run = %d %s", run.Code, run.Body.String())
	}
	accountCrons, ownerCrons := managedCronsByScope(fake)
	if len(accountCrons) != 1 {
		t.Fatalf("手动任务数 = %d，期望 1", len(accountCrons))
	}
	if !strings.Contains(accountCrons[0].TaskBefore, "export WECHAT_OPENIDS='oALICE0account0one'") {
		t.Fatalf("手动任务没有限定成单个账号: %q", accountCrons[0].TaskBefore)
	}
	if len(ownerCrons) != 0 {
		t.Fatalf("手动运行不应该建出定时任务: %+v", ownerCrons)
	}
}

// 管理员的「脚本目录可见范围」：非管理员看不到、也动不了白名单外的脚本；
// 管理员自己不受限。
func TestScriptDirVisibilityRestrictsNonAdmin(t *testing.T) {
	fake, server := newFakeQingLong(t)
	handler, app := newOwnershipQingLongApp(t, server.URL)
	adminCookie, aliceCookie, aliceID := seedConsoleUsers(t, handler)
	aliceAcc := seedAccount(t, app, "oALICE0account0one", &aliceID)

	// 非管理员不能碰这个设置
	forbidden := serveJSON(t, handler, http.MethodGet, "/api/qinglong/script-visibility", "", aliceCookie)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("普通用户读可见范围 = %d，期望 403", forbidden.Code)
	}

	// 默认不限制：非管理员能看到全部脚本
	before := serveJSON(t, handler, http.MethodGet, "/api/qinglong/jobs?ref="+refOf(aliceAcc), "", aliceCookie)
	beforePayload := decodeJobsPayload(t, before.Body.Bytes())
	if beforePayload.Restricted || beforePayload.Count != 5 {
		t.Fatalf("未配置白名单时 alice 应看到全部 5 个脚本，实际 restricted=%v count=%d",
			beforePayload.Restricted, beforePayload.Count)
	}

	// 管理员放行 code脚本 目录
	save := serveJSON(t, handler, http.MethodPut, "/api/qinglong/script-visibility",
		`{"dirs":["code脚本"]}`, adminCookie)
	if save.Code != http.StatusOK {
		t.Fatalf("保存可见范围 = %d %s", save.Code, save.Body.String())
	}

	after := serveJSON(t, handler, http.MethodGet, "/api/qinglong/jobs?ref="+refOf(aliceAcc), "", aliceCookie)
	afterPayload := decodeJobsPayload(t, after.Body.Bytes())
	if !afterPayload.Restricted {
		t.Fatal("受限后 restricted 应为 true")
	}
	if afterPayload.Count != 2 {
		t.Fatalf("alice 只应看到 code脚本 目录下的 2 个脚本，实际 %d（%+v）", afterPayload.Count, afterPayload.Jobs)
	}
	for _, job := range afterPayload.Jobs {
		if job.Dir != "code脚本" {
			t.Fatalf("列出了白名单外的脚本: %+v", job)
		}
	}
	if afterPayload.ScriptsTotal == afterPayload.Count {
		t.Fatalf("scripts_total 应保持未过滤的总数，实际 %d", afterPayload.ScriptsTotal)
	}

	// 白名单外的脚本：不能运行、也不能开定时（否则只是藏起来，调接口照样能跑）
	hidden := "SuperNaiBA_YYB-GO-Script/MDHY.js"
	runHidden := serveJSON(t, handler, http.MethodPost, "/api/qinglong/jobs/run",
		`{"ref":"`+refOf(aliceAcc)+`","script_key":"`+hidden+`"}`, aliceCookie)
	if runHidden.Code != http.StatusBadRequest {
		t.Fatalf("运行白名单外脚本 = %d，期望 400", runHidden.Code)
	}
	enableHidden := serveJSON(t, handler, http.MethodPut, "/api/qinglong/jobs/enable",
		`{"ref":"`+refOf(aliceAcc)+`","script_key":"`+hidden+`","enabled":true}`, aliceCookie)
	if enableHidden.Code != http.StatusBadRequest {
		t.Fatalf("给白名单外脚本开定时 = %d，期望 400", enableHidden.Code)
	}
	_, ownerCrons := managedCronsByScope(fake)
	if len(ownerCrons) != 0 {
		t.Fatalf("被拒的请求不应该建出任务: %+v", ownerCrons)
	}

	// 白名单内的脚本照常可以运行
	runAllowed := serveJSON(t, handler, http.MethodPost, "/api/qinglong/jobs/run",
		`{"ref":"`+refOf(aliceAcc)+`","script_key":"code脚本/绿鼻子.js"}`, aliceCookie)
	if runAllowed.Code != http.StatusAccepted {
		t.Fatalf("运行白名单内脚本 = %d %s", runAllowed.Code, runAllowed.Body.String())
	}

	// 管理员自己不受限
	adminView := serveJSON(t, handler, http.MethodGet, "/api/qinglong/jobs?ref="+refOf(aliceAcc), "", adminCookie)
	adminPayload := decodeJobsPayload(t, adminView.Body.Bytes())
	if adminPayload.Count != 5 || adminPayload.Restricted {
		t.Fatalf("管理员应看到全部脚本，实际 count=%d restricted=%v", adminPayload.Count, adminPayload.Restricted)
	}

	// 清空 = 取消限制
	clear := serveJSON(t, handler, http.MethodPut, "/api/qinglong/script-visibility", `{"dirs":[]}`, adminCookie)
	if clear.Code != http.StatusOK || !strings.Contains(clear.Body.String(), `"restricted":false`) {
		t.Fatalf("取消限制 = %d %s", clear.Code, clear.Body.String())
	}
}

// 登录账号级任务的命令拼装：一条任务覆盖该登录账号下的全部 openid。
func TestUserTaskSpecCoversEveryAccountInOneTask(t *testing.T) {
	app := &App{cfg: Config{QingLongServer: "yyb-go:8000"}}
	setting := &store.AccountPushSetting{Channel: "none"}
	accounts := []*store.WechatAccount{
		{ID: 3, OpenID: "oOWNER0three"},
		{ID: 9, OpenID: "oOWNER0nine"},
	}
	command, taskBefore, err := app.userTaskSpec(5, accounts[1], accounts, "code脚本/绿鼻子.js", setting)
	if err != nil {
		t.Fatalf("userTaskSpec() error = %v", err)
	}
	if command != "task code脚本/绿鼻子.js" {
		t.Fatalf("command = %q", command)
	}
	if !strings.Contains(taskBefore, "export WECHAT_OPENIDS='oOWNER0three,oOWNER0nine'") {
		t.Fatalf("任务前命令没有覆盖全部账号: %q", taskBefore)
	}
	// YYB_SERVER 记的是触发操作的那个账号（anchor），用于定位推送配置来源
	if !strings.Contains(taskBefore, "export YYB_SERVER='yyb-go:8000@9'") {
		t.Fatalf("任务前命令的 YYB_SERVER 不对: %q", taskBefore)
	}
	// 多账号是用逗号拼的，openid 里出现分隔符/引号必须直接拒绝
	for _, bad := range []string{"", "   ", "a,b", "a&b", "bad'; rm -rf /", "line\nbreak", "back\\slash"} {
		if _, _, err := app.userTaskSpec(5, nil,
			[]*store.WechatAccount{{ID: 1, OpenID: bad}}, "code脚本/绿鼻子.js", setting); err == nil {
			t.Fatalf("openid = %q 未被拒绝", bad)
		}
	}
	if _, _, err := app.userTaskSpec(5, nil, nil, "code脚本/绿鼻子.js", setting); err == nil {
		t.Fatal("该登录账号名下没有账号时应直接报错")
	}
	if !strings.HasPrefix(managedUserTaskName(5, "绿鼻子"), managedTaskPrefix) {
		t.Fatalf("登录账号级任务名必须带托管前缀: %q", managedUserTaskName(5, "绿鼻子"))
	}
}

// 老版本留下的「每个 code 账号一条、还启用着」的定时任务，会在打开运行页时
// 自动收敛成一条登录账号级任务（先建后停，中间不会出现一个都不跑的空窗）。
func TestLegacyPerAccountSchedulesAreConsolidated(t *testing.T) {
	fake, server := newFakeQingLong(t)
	handler, app := newOwnershipQingLongApp(t, server.URL)
	_, aliceCookie, aliceID := seedConsoleUsers(t, handler)

	ctx := context.Background()
	idA := seedAccount(t, app, "oALICE0account0one", &aliceID)
	idB := seedAccount(t, app, "oALICE0account0two", &aliceID)

	// 复刻 v9 的落库状态：两个账号各一条账号级任务，且都启用着
	catalog, err := app.scriptCatalog(ctx)
	if err != nil {
		t.Fatalf("scriptCatalog: %v", err)
	}
	for _, id := range []int64{idA, idB} {
		acc, err := app.db.GetAccount(ctx, id)
		if err != nil {
			t.Fatalf("GetAccount(%d): %v", id, err)
		}
		job, _, err := app.ensureAccountJob(ctx, acc, "code脚本/绿鼻子.js", catalog)
		if err != nil {
			t.Fatalf("ensureAccountJob(%d): %v", id, err)
		}
		if err := app.qinglong.setCronsEnabled(ctx, []int64{job.QLCronID}, true); err != nil {
			t.Fatalf("启用旧任务失败: %v", err)
		}
	}
	accountCrons, ownerCrons := managedCronsByScope(fake)
	if len(accountCrons) != 2 || len(ownerCrons) != 0 {
		t.Fatalf("前置状态不对：account=%d owner=%d", len(accountCrons), len(ownerCrons))
	}

	// 打开运行页触发收敛
	list := serveJSON(t, handler, http.MethodGet, "/api/qinglong/jobs?ref="+refOf(idA), "", aliceCookie)
	if list.Code != http.StatusOK {
		t.Fatalf("jobs = %d %s", list.Code, list.Body.String())
	}

	accountCrons, ownerCrons = managedCronsByScope(fake)
	if len(ownerCrons) != 1 {
		t.Fatalf("收敛后应只有 1 条登录账号级任务，实际 %d", len(ownerCrons))
	}
	if ownerCrons[0].IsDisabled == nil || *ownerCrons[0].IsDisabled != 0 {
		t.Fatalf("收敛出来的定时任务没有被启用: %+v", ownerCrons[0])
	}
	if !strings.Contains(ownerCrons[0].TaskBefore, "oALICE0account0one") ||
		!strings.Contains(ownerCrons[0].TaskBefore, "oALICE0account0two") {
		t.Fatalf("收敛后的任务没有覆盖该登录账号全部账号: %q", ownerCrons[0].TaskBefore)
	}
	for _, cron := range accountCrons {
		if cron.IsDisabled == nil || *cron.IsDisabled == 0 {
			t.Fatalf("旧的账号级任务没有被停用: %+v", cron)
		}
	}

	// 幂等：再看一次不会再多建任务
	serveJSON(t, handler, http.MethodGet, "/api/qinglong/jobs?ref="+refOf(idB), "", aliceCookie)
	_, ownerCrons = managedCronsByScope(fake)
	if len(ownerCrons) != 1 {
		t.Fatalf("重复打开页面把任务建多了：%d", len(ownerCrons))
	}
}

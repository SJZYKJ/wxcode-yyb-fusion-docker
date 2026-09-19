package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeQingLong struct {
	mu              sync.Mutex
	crons           []qingLongCron
	envs            []qingLongEnv
	scripts         []qingLongScriptNode
	scriptsStatus   int // /open/scripts/files 的返回码；0 视为 200，可设 404 模拟没有该端点的老版本
	nextCron        int64
	nextEnv         int64
	runIDs          []int64
	deletedIDs      []int64
	commands        []string
	taskBefores     []string
	logs            []qingLongLogEntry
	failDeleteCrons bool
}

func intPointer(value int) *int { return &value }

func newFakeQingLong(t *testing.T) (*fakeQingLong, *httptest.Server) {
	t.Helper()
	fake := &fakeQingLong{
		nextCron: 100,
		nextEnv:  50,
		envs:     []qingLongEnv{},
		crons: []qingLongCron{
			{ID: 1, Name: "美的会员", Command: "task SuperNaiBA_YYB-GO-Script/MDHY.js", Schedule: "11 8 * * *", Status: 1, IsDisabled: intPointer(1)},
			{ID: 2, Name: "EOOS", Command: "task SuperNaiBA_YYB-GO-Script/eoos/eoos_checkin.py", Schedule: "30 8 * * *", Status: 1, IsDisabled: intPointer(1)},
			{ID: 3, Name: "DT生活", Command: "task 525815266_YYB-Go-Enhanced/scripts/DTSH.py", Schedule: "48 15 * * *", Status: 1, IsDisabled: intPointer(1)},
		},
		scripts: fakeScriptTree(),
	}
	server := httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	t.Cleanup(server.Close)
	return fake, server
}

// 模拟青龙 /open/scripts/files 返回的脚本目录树。
//
// 故意混进一批「不该被挂给账号」的文件：共用工具、青龙自带通知、依赖目录，
// 以及非脚本文件、路径带 eoos 的检查脚本，用来验证过滤规则。
func fakeScriptTree() []qingLongScriptNode {
	return []qingLongScriptNode{
		{Title: "code脚本", Value: "code脚本", Children: []qingLongScriptNode{
			{Title: "绿鼻子.js", Value: "code脚本/绿鼻子.js", Parent: "code脚本"},
			{Title: "匠心中华.js", Value: "code脚本/匠心中华.js", Parent: "code脚本"},
			{Title: "notify.js", Value: "code脚本/notify.js", Parent: "code脚本"},
			{Title: "wechat_tools.js", Value: "code脚本/wechat_tools.js", Parent: "code脚本"},
			{Title: "!RunAll.py", Value: "code脚本/!RunAll.py", Parent: "code脚本"},
			{Title: "README.md", Value: "code脚本/README.md", Parent: "code脚本"},
		}},
		{Title: "SuperNaiBA_YYB-GO-Script", Value: "SuperNaiBA_YYB-GO-Script", Children: []qingLongScriptNode{
			{Title: "MDHY.js", Value: "SuperNaiBA_YYB-GO-Script/MDHY.js", Parent: "SuperNaiBA_YYB-GO-Script"},
			{Title: "eoos", Value: "SuperNaiBA_YYB-GO-Script/eoos", Children: []qingLongScriptNode{
				{Title: "eoos_checkin.py", Value: "SuperNaiBA_YYB-GO-Script/eoos/eoos_checkin.py", Parent: "SuperNaiBA_YYB-GO-Script/eoos"},
			}},
		}},
		{Title: "DTSH.py", Value: "525815266_YYB-Go-Enhanced/scripts/DTSH.py", Parent: "525815266_YYB-Go-Enhanced/scripts"},
		{Title: "美的会员副本.js", Value: "美的会员副本.js"},
		{Title: "SendNotify.py", Value: "SendNotify.py"},
		{Title: "node_modules", Value: "node_modules", Children: []qingLongScriptNode{
			{Title: "index.js", Value: "node_modules/some-pkg/index.js", Parent: "node_modules/some-pkg"},
		}},
	}
}

func (f *fakeQingLong) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	write := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": data}) }
	if r.URL.Path == "/open/auth/token" {
		write(map[string]any{"token": "fake-token", "expiration": 3600})
		return
	}
	if r.Header.Get("Authorization") != "Bearer fake-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && (r.URL.Path == "/open/scripts/files" || r.URL.Path == "/open/scripts"):
		if f.scriptsStatus != 0 && f.scriptsStatus != http.StatusOK {
			w.WriteHeader(f.scriptsStatus)
			write(nil)
			return
		}
		write(f.scripts)
	case r.Method == http.MethodGet && r.URL.Path == "/open/crons":
		write(f.crons)
	case r.Method == http.MethodPost && r.URL.Path == "/open/crons":
		var in qingLongCron
		_ = json.NewDecoder(r.Body).Decode(&in)
		in.ID = f.nextCron
		f.nextCron++
		in.Status = 1
		in.IsDisabled = intPointer(0)
		if in.LogName == "" {
			in.LogName = fmt.Sprintf("managed-%d", in.ID)
		}
		f.crons = append(f.crons, in)
		f.commands = append(f.commands, in.Command)
		f.taskBefores = append(f.taskBefores, in.TaskBefore)
		write(in)
	case r.Method == http.MethodPut && r.URL.Path == "/open/crons":
		var in qingLongCron
		_ = json.NewDecoder(r.Body).Decode(&in)
		for i := range f.crons {
			if f.crons[i].ID == in.ID {
				f.crons[i].Name, f.crons[i].Command, f.crons[i].Schedule, f.crons[i].TaskBefore, f.crons[i].LogName = in.Name, in.Command, in.Schedule, in.TaskBefore, in.LogName
				f.commands = append(f.commands, in.Command)
				f.taskBefores = append(f.taskBefores, in.TaskBefore)
			}
		}
		write(nil)
	case r.Method == http.MethodPut && (r.URL.Path == "/open/crons/enable" || r.URL.Path == "/open/crons/disable"):
		var ids []int64
		_ = json.NewDecoder(r.Body).Decode(&ids)
		disabled := 1
		if strings.HasSuffix(r.URL.Path, "/enable") {
			disabled = 0
		}
		for i := range f.crons {
			for _, id := range ids {
				if f.crons[i].ID == id {
					f.crons[i].IsDisabled = intPointer(disabled)
				}
			}
		}
		write(nil)
	case r.Method == http.MethodPut && r.URL.Path == "/open/crons/run":
		var ids []int64
		_ = json.NewDecoder(r.Body).Decode(&ids)
		f.runIDs = append(f.runIDs, ids...)
		for i := range f.crons {
			for _, id := range ids {
				if f.crons[i].ID != id {
					continue
				}
				filename := "2026-07-31-14-30-00-000.log"
				key := f.crons[i].LogName + "/" + filename
				f.crons[i].LogPath = key
				f.crons[i].LastExecutionTime = 1785480000
				f.logs = append(f.logs, qingLongLogEntry{Title: f.crons[i].LogName, Key: f.crons[i].LogName, Type: "directory", Children: []qingLongLogEntry{{Title: filename, Key: key, Parent: f.crons[i].LogName, Type: "file", Size: 88, CreateTime: 1785480000000}}})
			}
		}
		write(nil)
	case r.Method == http.MethodDelete && r.URL.Path == "/open/crons":
		if f.failDeleteCrons {
			w.WriteHeader(http.StatusBadGateway)
			write(nil)
			return
		}
		var ids []int64
		_ = json.NewDecoder(r.Body).Decode(&ids)
		f.deletedIDs = append(f.deletedIDs, ids...)
		kept := f.crons[:0]
		for _, cron := range f.crons {
			deleted := false
			for _, id := range ids {
				if cron.ID == id {
					deleted = true
					break
				}
			}
			if !deleted {
				kept = append(kept, cron)
			}
		}
		f.crons = kept
		write(nil)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/log"):
		write("fake account log")
	case r.Method == http.MethodGet && r.URL.Path == "/open/logs":
		write(f.logs)
	case r.Method == http.MethodGet && r.URL.Path == "/open/logs/detail":
		write("fake account history log")
	case r.Method == http.MethodGet && r.URL.Path == "/open/envs":
		write(f.envs)
	case r.Method == http.MethodPost && r.URL.Path == "/open/envs":
		var in []qingLongEnv
		_ = json.NewDecoder(r.Body).Decode(&in)
		for i := range in {
			in[i].ID = f.nextEnv
			f.nextEnv++
			f.envs = append(f.envs, in[i])
		}
		write(in)
	case r.Method == http.MethodPut && r.URL.Path == "/open/envs":
		var in qingLongEnv
		_ = json.NewDecoder(r.Body).Decode(&in)
		for i := range f.envs {
			if f.envs[i].ID == in.ID {
				f.envs[i].Name, f.envs[i].Value, f.envs[i].Remarks = in.Name, in.Value, in.Remarks
			}
		}
		write(nil)
	case r.Method == http.MethodPut && (r.URL.Path == "/open/envs/enable" || r.URL.Path == "/open/envs/disable"):
		write(nil)
	default:
		w.WriteHeader(http.StatusNotFound)
		write(nil)
	}
}

func newRunsTestApp(t *testing.T, qlURL string) (*App, http.Handler, string) {
	t.Helper()
	app, err := NewApp(Config{
		ResourceRoot:     t.TempDir(),
		RequestTimeout:   time.Second,
		SessionTTL:       time.Minute,
		QRSessionTTL:     time.Minute,
		QingLongURL:      qlURL,
		QingLongClientID: "client-id",
		QingLongSecret:   "client-secret",
		QingLongServer:   "yyb-go:8000",
	})
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	status := "alive"
	acc, err := app.db.UpsertAccount(context.Background(), "test-openid", "buffer", nil, nil, nil, nil, nil, &status, nil)
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return app, app.Handler(), fmt.Sprintf("%d", acc.ID)
}

func TestEnhancedRepoScriptsKeepTheirSourcePath(t *testing.T) {
	fake, server := newFakeQingLong(t)
	_, handler, ref := newRunsTestApp(t, server.URL)

	list := apiRequest(t, handler, http.MethodGet, "/api/qinglong/jobs?ref="+url.QueryEscape(ref), nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "DTSH.py") {
		t.Fatalf("enhanced repo script missing: %d %s", list.Code, list.Body.String())
	}

	enable := apiRequest(t, handler, http.MethodPut, "/api/qinglong/jobs/enable", map[string]any{
		"ref": ref, "script_key": "DTSH.py", "enabled": true,
	})
	if enable.Code != http.StatusOK {
		t.Fatalf("enable response = %d %s", enable.Code, enable.Body.String())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := fake.commands[len(fake.commands)-1]; got != "task 525815266_YYB-Go-Enhanced/scripts/DTSH.py" {
		t.Fatalf("managed command = %q", got)
	}
}

// 端到端：脚本池 = 青龙脚本目录里的脚本文件，而不是定时任务。
//
// 回归背景：列表曾经从「全部定时任务」反推，于是同时踩两个坑 —— 用户自己建的
// 任务被当成可用脚本，而网关给账号建的托管任务又得反过来排除；更糟的是同一个
// 脚本在青龙里已有全局任务时，再挂一个账号任务会真的跑两遍。
// 「能跑什么」应该看脚本文件，定时任务只回答「这个账号挂了这个脚本没有」。
func TestScriptCatalogUsesScriptFiles(t *testing.T) {
	fake, server := newFakeQingLong(t)
	_, handler, ref := newRunsTestApp(t, server.URL)

	list := apiRequest(t, handler, http.MethodGet, "/api/qinglong/jobs?ref="+url.QueryEscape(ref), nil)
	if list.Code != http.StatusOK {
		t.Fatalf("jobs response = %d %s", list.Code, list.Body.String())
	}
	payload := decodeJobsPayload(t, list.Body.Bytes())

	if payload.ScriptSource != "scripts" {
		t.Fatalf("脚本池应来自脚本文件, got %q (degraded=%q)", payload.ScriptSource, payload.Degraded)
	}
	// 脚本目录里一共 12 个文件，其中能挂给账号跑的只有 5 个
	if payload.ScriptsTotal != 12 {
		t.Fatalf("scripts_total = %d, want 12", payload.ScriptsTotal)
	}
	want := map[string]bool{
		"code脚本/绿鼻子.js":                             false,
		"code脚本/匠心中华.js":                            false,
		"SuperNaiBA_YYB-GO-Script/MDHY.js":          false,
		"525815266_YYB-Go-Enhanced/scripts/DTSH.py": false,
		"美的会员副本.js":                                 false,
	}
	active := make(map[string]bool, len(payload.Jobs))
	for _, job := range payload.Jobs {
		if _, ok := want[job.ScriptKey]; !ok {
			t.Fatalf("不该出现在脚本池里: %q", job.ScriptKey)
		}
		want[job.ScriptKey] = true
		active[job.ScriptKey] = job.GlobalTaskActive
	}
	for key, seen := range want {
		if !seen {
			t.Fatalf("脚本 %s 没有被列出", key)
		}
	}
	// fixture 里那条全局任务处于停用状态，所以还不算「会重复跑」
	if active["code脚本/绿鼻子.js"] || active["SuperNaiBA_YYB-GO-Script/MDHY.js"] {
		t.Fatalf("停用的全局任务不该被标注为会重复执行: %v", active)
	}
	// 启用之后就要标出来：再挂账号任务就是跑两遍
	fake.mu.Lock()
	fake.crons[0].IsDisabled = intPointer(0)
	fake.mu.Unlock()
	again := decodeJobsPayload(t, apiRequest(t, handler, http.MethodGet, "/api/qinglong/jobs?ref="+url.QueryEscape(ref), nil).Body.Bytes())
	flagged := false
	for _, job := range again.Jobs {
		if job.ScriptKey == "SuperNaiBA_YYB-GO-Script/MDHY.js" {
			flagged = job.GlobalTaskActive
		}
	}
	if !flagged {
		t.Fatal("全局任务启用后应被标注 global_task_active，否则用户看不出会跑两遍")
	}

	// 勾选脚本时命令必须保留完整相对路径（含中文目录）
	enable := apiRequest(t, handler, http.MethodPut, "/api/qinglong/jobs/enable", map[string]any{
		"ref": ref, "script_key": "code脚本/绿鼻子.js", "enabled": true,
	})
	if enable.Code != http.StatusOK {
		t.Fatalf("enable response = %d %s", enable.Code, enable.Body.String())
	}
	fake.mu.Lock()
	if got := fake.commands[len(fake.commands)-1]; got != "task code脚本/绿鼻子.js" {
		fake.mu.Unlock()
		t.Fatalf("managed command = %q", got)
	}
	fake.mu.Unlock()

	// 脚本在根目录时命令就是裸脚本名，不补任何前缀
	enable = apiRequest(t, handler, http.MethodPut, "/api/qinglong/jobs/enable", map[string]any{
		"ref": ref, "script_key": "美的会员副本.js", "enabled": true,
	})
	if enable.Code != http.StatusOK {
		t.Fatalf("enable response = %d %s", enable.Code, enable.Body.String())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := fake.commands[len(fake.commands)-1]; got != "task 美的会员副本.js" {
		t.Fatalf("managed command = %q", got)
	}
}

// 面板没有脚本文件接口时（老青龙、代代面板），退回从定时任务反推，并在响应里说明来源。
func TestScriptCatalogFallsBackToCronTasks(t *testing.T) {
	fake, server := newFakeQingLong(t)
	fake.mu.Lock()
	fake.scriptsStatus = http.StatusNotFound
	fake.mu.Unlock()

	_, handler, ref := newRunsTestApp(t, server.URL)
	list := apiRequest(t, handler, http.MethodGet, "/api/qinglong/jobs?ref="+url.QueryEscape(ref), nil)
	if list.Code != http.StatusOK {
		t.Fatalf("jobs response = %d %s", list.Code, list.Body.String())
	}
	payload := decodeJobsPayload(t, list.Body.Bytes())
	if payload.ScriptSource != "crons" {
		t.Fatalf("script_source = %q, want crons", payload.ScriptSource)
	}
	if payload.Degraded == "" {
		t.Fatal("降级时应说明原因，页面才能提示用户")
	}
	keys := make(map[string]bool, len(payload.Jobs))
	for _, job := range payload.Jobs {
		keys[job.ScriptKey] = true
	}
	// 三个任务里能解析出脚本的只有两个；eoos_checkin.py 与网关自建任务都不算
	if len(keys) != 2 || !keys["SuperNaiBA_YYB-GO-Script/MDHY.js"] || !keys["525815266_YYB-Go-Enhanced/scripts/DTSH.py"] {
		t.Fatalf("降级后应列出定时任务里的 2 个脚本, got %v", keys)
	}
}

// 库里存的历史 script_key 是裸文件名（v7.1 及更早的脚本池 key），要能归一到新写法。
func TestMatchScriptKeyAcceptsLegacyBareName(t *testing.T) {
	sources := []scriptSource{
		{Key: "code脚本/绿鼻子.js", Name: "绿鼻子.js"},
		{Key: "其他/绿鼻子.js", Name: "绿鼻子.js"},
		{Key: "美的会员副本.js", Name: "美的会员副本.js"},
	}
	if key, ok := matchScriptKey(sources, "code脚本/绿鼻子.js"); !ok || key != "code脚本/绿鼻子.js" {
		t.Fatalf("精确匹配失败: ok=%v key=%q", ok, key)
	}
	if _, ok := matchScriptKey(sources, "/code脚本/绿鼻子.js"); !ok {
		t.Fatal("带前导斜杠的 key 应被容忍")
	}
	// 同名脚本不止一个时不能猜，否则会给账号挂错文件
	if key, ok := matchScriptKey(sources, "绿鼻子.js"); ok {
		t.Fatalf("同名脚本不唯一时不该回退匹配: %q", key)
	}
	if key, ok := matchScriptKey(sources, "美的会员副本.js"); !ok || key != "美的会员副本.js" {
		t.Fatalf("唯一同名回退失败: ok=%v key=%q", ok, key)
	}
	if _, ok := matchScriptKey(sources, "不存在的脚本.js"); ok {
		t.Fatal("池子里没有的脚本不该匹配成功")
	}
}

func TestParseScriptPathFromCron(t *testing.T) {
	cases := []struct {
		desc  string
		cron  qingLongCron
		path  string
		valid bool
	}{
		{"中文目录", qingLongCron{Name: "绿鼻子", Command: "task code脚本/绿鼻子.js"}, "code脚本/绿鼻子.js", true},
		{"多级目录", qingLongCron{Name: "DT生活", Command: "task 525815266_YYB-Go-Enhanced/scripts/DTSH.py"}, "525815266_YYB-Go-Enhanced/scripts/DTSH.py", true},
		{"无目录", qingLongCron{Name: "美的", Command: "task 美的会员.js"}, "美的会员.js", true},
		{"node 绝对路径", qingLongCron{Name: "美的", Command: "node /ql/scripts/美的会员.js"}, "美的会员.js", true},
		{"python 绝对路径带中文目录", qingLongCron{Name: "绿鼻子", Command: "python3 /ql/scripts/code脚本/绿鼻子.py"}, "code脚本/绿鼻子.py", true},
		{"追加参数", qingLongCron{Name: "美的", Command: "task 美的会员.js now"}, "美的会员.js", true},
		{"带引号", qingLongCron{Name: "美的", Command: `task "code脚本/绿鼻子.js"`}, "code脚本/绿鼻子.js", true},
		{"带重定向", qingLongCron{Name: "美的", Command: "node /ql/scripts/美的会员.js >/dev/null 2>&1"}, "美的会员.js", true},
		{"反斜杠分隔", qingLongCron{Name: "美的", Command: `task code脚本\美的会员.js`}, "code脚本/美的会员.js", true},
		{"网关自建的账号任务", qingLongCron{Name: "[YYB:1] 主用 · 绿鼻子", Command: "task code脚本/绿鼻子.js"}, "", false},
		{"被忽略的推送脚本", qingLongCron{Name: "推送", Command: "task SendNotify.py"}, "", false},
		{"共用工具", qingLongCron{Name: "工具", Command: "task code脚本/wechat_tools.js"}, "", false},
		{"依赖目录", qingLongCron{Name: "依赖", Command: "task node_modules/pkg/index.js"}, "", false},
		{"非脚本命令", qingLongCron{Name: "拉库", Command: "ql repo https://github.com/x/y.git"}, "", false},
		{"空命令", qingLongCron{Name: "空", Command: ""}, "", false},
		{"路径穿越", qingLongCron{Name: "坏", Command: "task ../evil/x.js"}, "", false},
		{"shell 元字符", qingLongCron{Name: "坏", Command: "task code脚本;rm -rf /x.js"}, "", false},
	}
	for _, tc := range cases {
		path, ok := parseScriptPathFromCron(tc.cron)
		if ok != tc.valid {
			t.Fatalf("%s: ok = %v, want %v (path=%q)", tc.desc, ok, tc.valid, path)
		}
		if ok && path != tc.path {
			t.Fatalf("%s: got path=%q, want %q", tc.desc, path, tc.path)
		}
	}
}

func apiRequest(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

type jobsPayload struct {
	Count        int    `json:"count"`
	ScriptSource string `json:"script_source"`
	ScriptsTotal int    `json:"scripts_total"`
	CronTotal    int    `json:"cron_total"`
	Degraded     string `json:"degraded_note"`
	Jobs         []struct {
		ScriptKey        string `json:"script_key"`
		Name             string `json:"name"`
		Dir              string `json:"dir"`
		Schedule         string `json:"schedule"`
		Provisioned      bool   `json:"provisioned"`
		Enabled          bool   `json:"enabled"`
		GlobalTaskActive bool   `json:"global_task_active"`
	} `json:"jobs"`
}

func decodeJobsPayload(t *testing.T, raw []byte) jobsPayload {
	t.Helper()
	// 接口响应统一带 {code,msg,data} 外壳
	var envelope struct {
		Data jobsPayload `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("解析 jobs 响应失败: %v (%s)", err, raw)
	}
	return envelope.Data
}

func TestAccountJobsAreIsolatedDisabledByDefaultAndRunExplicitly(t *testing.T) {
	fake, server := newFakeQingLong(t)
	_, handler, ref := newRunsTestApp(t, server.URL)

	list := apiRequest(t, handler, http.MethodGet, "/api/qinglong/jobs?ref="+url.QueryEscape(ref), nil)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "eoos_checkin") {
		t.Fatalf("initial jobs response = %d %s", list.Code, list.Body.String())
	}
	if !strings.Contains(list.Body.String(), "MDHY.js") {
		t.Fatalf("compatible script missing: %s", list.Body.String())
	}

	enable := apiRequest(t, handler, http.MethodPut, "/api/qinglong/jobs/enable", map[string]any{
		"ref": ref, "script_key": "SuperNaiBA_YYB-GO-Script/MDHY.js", "enabled": true,
	})
	if enable.Code != http.StatusOK {
		t.Fatalf("enable response = %d %s", enable.Code, enable.Body.String())
	}
	fake.mu.Lock()
	if len(fake.runIDs) != 0 {
		t.Fatalf("enabling a task unexpectedly ran it: %v", fake.runIDs)
	}
	if len(fake.commands) == 0 {
		t.Fatal("managed task command was not created")
	}
	command := fake.commands[len(fake.commands)-1]
	fake.mu.Unlock()
	if command != "task SuperNaiBA_YYB-GO-Script/MDHY.js" {
		t.Fatalf("managed command = %q", command)
	}
	taskBefore := fake.taskBefores[len(fake.taskBefores)-1]
	if !strings.Contains(taskBefore, "export YYB_SERVER='yyb-go:8000@"+ref+"'") {
		t.Fatalf("managed task_before = %q", taskBefore)
	}

	run := apiRequest(t, handler, http.MethodPost, "/api/qinglong/jobs/run", map[string]any{
		"ref": ref, "script_key": "SuperNaiBA_YYB-GO-Script/MDHY.js",
	})
	if run.Code != http.StatusAccepted {
		t.Fatalf("run response = %d %s", run.Code, run.Body.String())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.runIDs) != 1 {
		t.Fatalf("explicit run IDs = %v", fake.runIDs)
	}
}

func TestAccountJobUsesCurrentQingLongStateFields(t *testing.T) {
	fake, server := newFakeQingLong(t)
	_, handler, ref := newRunsTestApp(t, server.URL)
	_ = apiRequest(t, handler, http.MethodPut, "/api/qinglong/jobs/enable", map[string]any{
		"ref": ref, "script_key": "SuperNaiBA_YYB-GO-Script/MDHY.js", "enabled": true,
	})

	fake.mu.Lock()
	fake.crons[0].IsDisabled = intPointer(0)
	for i := range fake.crons {
		if strings.HasPrefix(fake.crons[i].Name, "[YYB:") {
			fake.crons[i].Status = 1
			fake.crons[i].PID = 12345
			fake.crons[i].IsDisabled = intPointer(0)
		}
	}
	fake.mu.Unlock()

	idle := apiRequest(t, handler, http.MethodGet, "/api/qinglong/jobs?ref="+url.QueryEscape(ref), nil)
	if idle.Code != http.StatusOK || !strings.Contains(idle.Body.String(), `"enabled":true`) || !strings.Contains(idle.Body.String(), `"running":false`) {
		t.Fatalf("idle current QingLong job response = %d %s", idle.Code, idle.Body.String())
	}
	// 列表里的条目应该是脚本本身（名字取自脚本文件），而不是青龙里那条全局任务
	if !strings.Contains(idle.Body.String(), `"name":"MDHY"`) || strings.Contains(idle.Body.String(), `"name":"[YYB:`) {
		t.Fatalf("managed account task replaced the source script: %s", idle.Body.String())
	}
	if !strings.Contains(idle.Body.String(), `"global_task_active":true`) {
		t.Fatalf("current QingLong enabled source was not detected: %s", idle.Body.String())
	}

	fake.mu.Lock()
	for i := range fake.crons {
		if strings.HasPrefix(fake.crons[i].Name, "[YYB:") {
			fake.crons[i].Status = 0.5
		}
	}
	fake.mu.Unlock()

	queued := apiRequest(t, handler, http.MethodGet, "/api/qinglong/jobs?ref="+url.QueryEscape(ref), nil)
	if queued.Code != http.StatusOK || !strings.Contains(queued.Body.String(), `"running":true`) {
		t.Fatalf("queued current QingLong job response = %d %s", queued.Code, queued.Body.String())
	}
}

func TestAccountRunHistoryAndLogAreScopedToAccount(t *testing.T) {
	_, server := newFakeQingLong(t)
	app, handler, ref := newRunsTestApp(t, server.URL)
	run := apiRequest(t, handler, http.MethodPost, "/api/qinglong/jobs/run", map[string]any{
		"ref": ref, "script_key": "SuperNaiBA_YYB-GO-Script/MDHY.js",
	})
	if run.Code != http.StatusAccepted || !strings.Contains(run.Body.String(), `"account_id":1`) {
		t.Fatalf("run response = %d %s", run.Code, run.Body.String())
	}

	firstLogKey := managedLogName(1, "SuperNaiBA_YYB-GO-Script/MDHY.js") + "/2026-07-31-14-30-00-000.log"
	history := apiRequest(t, handler, http.MethodGet, "/api/qinglong/runs?ref="+url.QueryEscape(ref), nil)
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), `"script_key":"SuperNaiBA_YYB-GO-Script/MDHY.js"`) || !strings.Contains(history.Body.String(), `"log_key":"`+firstLogKey+`"`) {
		t.Fatalf("account history response = %d %s", history.Code, history.Body.String())
	}

	logKey := url.QueryEscape(firstLogKey)
	log := apiRequest(t, handler, http.MethodGet, "/api/qinglong/runs/log?ref="+url.QueryEscape(ref)+"&log_key="+logKey, nil)
	if log.Code != http.StatusOK || !strings.Contains(log.Body.String(), "fake account history log") {
		t.Fatalf("account log response = %d %s", log.Code, log.Body.String())
	}

	status := "alive"
	second, err := app.db.UpsertAccount(context.Background(), "second-openid", "buffer", nil, nil, nil, nil, nil, &status, nil)
	if err != nil {
		t.Fatalf("seed second account: %v", err)
	}
	secondRef := fmt.Sprintf("%d", second.ID)
	secondRun := apiRequest(t, handler, http.MethodPost, "/api/qinglong/jobs/run", map[string]any{"ref": secondRef, "script_key": "SuperNaiBA_YYB-GO-Script/MDHY.js"})
	if secondRun.Code != http.StatusAccepted {
		t.Fatalf("second account run response = %d %s", secondRun.Code, secondRun.Body.String())
	}
	secondLogKey := managedLogName(second.ID, "SuperNaiBA_YYB-GO-Script/MDHY.js") + "/2026-07-31-14-30-00-000.log"
	secondHistory := apiRequest(t, handler, http.MethodGet, "/api/qinglong/runs?ref="+url.QueryEscape(secondRef), nil)
	if secondHistory.Code != http.StatusOK || !strings.Contains(secondHistory.Body.String(), secondLogKey) || strings.Contains(secondHistory.Body.String(), firstLogKey) {
		t.Fatalf("second account history was not isolated = %d %s", secondHistory.Code, secondHistory.Body.String())
	}

	foreign := apiRequest(t, handler, http.MethodGet, "/api/qinglong/runs/log?ref="+url.QueryEscape(ref)+"&log_key="+url.QueryEscape(secondLogKey), nil)
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign account log response = %d %s", foreign.Code, foreign.Body.String())
	}
}

func TestPushSecretStaysInQingLongEnvironment(t *testing.T) {
	fake, server := newFakeQingLong(t)
	_, handler, ref := newRunsTestApp(t, server.URL)
	_ = apiRequest(t, handler, http.MethodPut, "/api/qinglong/jobs/enable", map[string]any{
		"ref": ref, "script_key": "SuperNaiBA_YYB-GO-Script/MDHY.js", "enabled": true,
	})
	secret := "SCT_FAKE_SECRET_VALUE"
	save := apiRequest(t, handler, http.MethodPut, "/api/qinglong/push", map[string]any{
		"ref": ref, "channel": "serverchan", "token": secret,
	})
	if save.Code != http.StatusOK {
		t.Fatalf("save push response = %d %s", save.Code, save.Body.String())
	}
	if strings.Contains(save.Body.String(), secret) || strings.Contains(save.Body.String(), "token_env_name") {
		t.Fatalf("push response leaked secret metadata: %s", save.Body.String())
	}
	if !strings.Contains(save.Body.String(), `"token_configured":true`) {
		t.Fatalf("push response does not report configured state: %s", save.Body.String())
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	foundSecret := false
	for _, env := range fake.envs {
		if env.Value == secret {
			foundSecret = true
		}
	}
	if !foundSecret {
		t.Fatal("secret was not stored in QingLong environment")
	}
	for _, command := range fake.commands {
		if strings.Contains(command, secret) {
			t.Fatalf("task command leaked secret: %q", command)
		}
	}
	for _, taskBefore := range fake.taskBefores {
		if strings.Contains(taskBefore, secret) {
			t.Fatalf("task_before leaked secret: %q", taskBefore)
		}
	}
	if !strings.Contains(fake.taskBefores[len(fake.taskBefores)-1], "${YYB_RUN_ACCOUNT_"+ref+"_SERVERCHAN_KEY:-}") {
		t.Fatalf("task_before does not reference account environment: %q", fake.taskBefores[len(fake.taskBefores)-1])
	}
}

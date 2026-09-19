package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"yyb_go/internal/store"
)

var validScriptKey = regexp.MustCompile(`^[\p{L}\p{N}_+./-]+\.(?:js|py)$`)

// 网关给账号建的托管任务都用这个前缀命名，用来和用户自己建的任务区分开。
const managedTaskPrefix = "[YYB:"

// 给账号新建任务时用的默认调度。脚本在青龙里已有同名全局任务时优先沿用它的，
// 所以这个值只在「这个脚本从来没有过定时任务」时才会用到。
const defaultAccountTaskSchedule = "0 9 * * *"

type scriptSource struct {
	Key      string // 脚本相对路径，如 code脚本/绿鼻子.js；也是 account_script_jobs.script_key
	Name     string // 文件名，如 绿鼻子.js
	Dir      string // 所在目录；脚本直接放在脚本根目录时为空
	Schedule string // 给账号建任务时用的调度
	// 青龙里跑同一个脚本、且不是网关托管的任务。它和账号任务会各跑一遍，
	// 页面上据此提示「重复执行」，但不阻止用户操作。
	GlobalCron *qingLongCron
}

// 展示用脚本名：去掉 .js/.py 后缀，任务名和列表里都更好读。
func (s scriptSource) displayName() string {
	if ext := filepath.Ext(s.Name); ext != "" {
		return strings.TrimSuffix(s.Name, ext)
	}
	return s.Name
}

type accountJobPublic struct {
	ScriptKey        string `json:"script_key"`
	Name             string `json:"name"`
	Schedule         string `json:"schedule"`
	Dir              string `json:"dir,omitempty"`
	Provisioned      bool   `json:"provisioned"`
	Enabled          bool   `json:"enabled"`
	Running          bool   `json:"running"`
	QLCronID         int64  `json:"ql_cron_id,omitempty"`
	LastExecutionAt  int64  `json:"last_execution_at"`
	LastRunningTime  int64  `json:"last_running_time"`
	GlobalTaskActive bool   `json:"global_task_active"`
}

type jobActionIn struct {
	Ref       string `json:"ref"`
	ScriptKey string `json:"script_key"`
	Enabled   bool   `json:"enabled"`
}

type pushSettingIn struct {
	Ref     string  `json:"ref"`
	Channel string  `json:"channel"`
	Token   string  `json:"token"`
	Topic   *string `json:"topic"`
}

type pushSettingPublic struct {
	Channel         string `json:"channel"`
	TokenConfigured bool   `json:"token_configured"`
	TopicConfigured bool   `json:"topic_configured"`
}

type accountRunPublic struct {
	AccountID  int64  `json:"account_id"`
	ScriptKey  string `json:"script_key"`
	Name       string `json:"name"`
	QLCronID   int64  `json:"ql_cron_id"`
	LogKey     string `json:"log_key"`
	StartedAt  int64  `json:"started_at"`
	Size       int64  `json:"size"`
	Running    bool   `json:"running"`
	TaskStatus string `json:"status"`
}

func (a *App) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	serveFileOrText(w, r, filepath.Join(a.resources.Templates, "runs.html"), fallbackRunsHTML)
}

func (a *App) handleQingLongStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !a.qinglong.configured() {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "connected": false})
		return
	}
	if err := a.qinglong.status(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": true, "connected": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "connected": true})
}

func (a *App) handleQingLongJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	acc, ok := a.resolveAccountFromQuery(w, r)
	if !ok {
		return
	}
	catalog, err := a.scriptCatalog(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	sources, cronsByID := catalog.Sources, catalog.CronsByID
	storedJobs, err := a.db.ListAccountScriptJobs(r.Context(), acc.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 库里存的 key 可能是旧写法的裸文件名（见 matchScriptKey），先归一到当前池子
	jobsByKey := make(map[string]store.AccountScriptJob, len(storedJobs))
	for _, job := range storedJobs {
		if key, ok := matchScriptKey(sources, job.ScriptKey); ok {
			jobsByKey[key] = job
			continue
		}
		jobsByKey[job.ScriptKey] = job
	}
	out := make([]accountJobPublic, 0, len(sources))
	for _, source := range sources {
		item := accountJobPublic{
			ScriptKey:        source.Key,
			Name:             source.displayName(),
			Dir:              source.Dir,
			Schedule:         source.Schedule,
			GlobalTaskActive: source.GlobalCron != nil && source.GlobalCron.enabled(),
		}
		if job, exists := jobsByKey[source.Key]; exists {
			if cron, found := cronsByID[job.QLCronID]; found {
				item.Provisioned = true
				item.Enabled = cron.enabled()
				item.Running = cron.running()
				item.QLCronID = cron.ID
				item.LastExecutionAt = cron.getLastExecutionAt()
				item.LastRunningTime = cron.getLastRunningTime()
			}
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account": acc.Public(),
		"jobs":    out,
		"count":   len(out),
		// 列表为空时页面要区分几种原因，所以把「数据源」「读到了多少条」都带上：
		//   script_source=scripts 时 total 是脚本文件数（排除依赖/工具脚本后的可挂载数见 count）
		//   script_source=crons   时说明面板没有脚本接口，退回从定时任务反推
		"script_source": catalog.Source,
		"scripts_total": catalog.Total,
		"degraded_note": catalog.Degraded,
		"cron_total":    len(cronsByID),
	})
}

// 按页面传来的 script_key 找这个账号已配置的任务。
//
// 库里存的可能是旧写法的裸文件名（v7.1 及更早脚本池的 key 就是裸文件名），
// 所以查不到时再用脚本池归一化一次；两次都找不到才算没配过。
func (a *App) resolveAccountScriptJob(ctx context.Context, acc *store.WechatAccount, scriptKey string) (*store.AccountScriptJob, error) {
	scriptKey = strings.TrimSpace(scriptKey)
	job, err := a.db.GetAccountScriptJob(ctx, acc.ID, scriptKey)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return job, err
	}
	catalog, catalogErr := a.scriptCatalog(ctx)
	if catalogErr != nil {
		return nil, err
	}
	resolved, ok := matchScriptKey(catalog.Sources, scriptKey)
	if !ok {
		return nil, err
	}
	return a.db.GetAccountScriptJob(ctx, acc.ID, resolved)
}

func (a *App) handleQingLongJobEnable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body jobActionIn
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	acc, ok := a.resolveAccountRef(w, r, body.Ref)
	if !ok {
		return
	}
	if !body.Enabled {
		job, err := a.resolveAccountScriptJob(r.Context(), acc, body.ScriptKey)
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "provisioned": false})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := a.qinglong.setCronsEnabled(r.Context(), []int64{job.QLCronID}, false); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "provisioned": true, "ql_cron_id": job.QLCronID})
		return
	}
	job, _, err := a.ensureAccountJob(r.Context(), acc, body.ScriptKey)
	if err != nil {
		writeRunError(w, err)
		return
	}
	if err := a.qinglong.setCronsEnabled(r.Context(), []int64{job.QLCronID}, true); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "provisioned": true, "ql_cron_id": job.QLCronID})
}

func (a *App) handleQingLongJobRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body jobActionIn
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	acc, ok := a.resolveAccountRef(w, r, body.Ref)
	if !ok {
		return
	}
	job, source, err := a.ensureAccountJob(r.Context(), acc, body.ScriptKey)
	if err != nil {
		writeRunError(w, err)
		return
	}
	if err := a.qinglong.runCrons(r.Context(), []int64{job.QLCronID}); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true, "account_id": acc.ID, "script_key": source.Key, "ql_cron_id": job.QLCronID, "name": source.displayName(), "submitted_at": time.Now().Unix()})
}

func (a *App) handleQingLongJobLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	acc, ok := a.resolveAccountFromQuery(w, r)
	if !ok {
		return
	}
	scriptKey := strings.TrimSpace(r.URL.Query().Get("script_key"))
	job, err := a.resolveAccountScriptJob(r.Context(), acc, scriptKey)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "该账号尚未创建此脚本任务")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	logText, err := a.qinglong.cronLog(r.Context(), job.QLCronID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"script_key": scriptKey, "ql_cron_id": job.QLCronID, "log": logText})
}

func (a *App) handleQingLongRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	acc, ok := a.resolveAccountFromQuery(w, r)
	if !ok {
		return
	}
	runs, err := a.accountRunHistory(r.Context(), acc.ID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": acc.Public(), "runs": runs, "count": len(runs)})
}

func (a *App) handleQingLongRunLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	acc, ok := a.resolveAccountFromQuery(w, r)
	if !ok {
		return
	}
	logKey := strings.TrimSpace(r.URL.Query().Get("log_key"))
	runs, err := a.accountRunHistory(r.Context(), acc.ID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	var selected *accountRunPublic
	for i := range runs {
		if runs[i].LogKey == logKey {
			selected = &runs[i]
			break
		}
	}
	if selected == nil {
		writeError(w, http.StatusNotFound, "该日志不属于当前账号或已被清理")
		return
	}
	separator := strings.LastIndex(logKey, "/")
	if separator <= 0 || separator == len(logKey)-1 {
		writeError(w, http.StatusBadRequest, "日志路径不合法")
		return
	}
	logText, err := a.qinglong.logDetail(r.Context(), logKey[:separator], logKey[separator+1:])
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account_id": acc.ID, "script_key": selected.ScriptKey, "log_key": logKey, "log": logText})
}

func (a *App) accountRunHistory(ctx context.Context, accountID int64) ([]accountRunPublic, error) {
	catalog, err := a.scriptCatalog(ctx)
	if err != nil {
		return nil, err
	}
	sources, cronsByID := catalog.Sources, catalog.CronsByID
	jobs, err := a.db.ListAccountScriptJobs(ctx, accountID)
	if err != nil {
		return nil, err
	}
	logs, err := a.qinglong.listLogs(ctx)
	if err != nil {
		return nil, err
	}
	sourceByKey := make(map[string]scriptSource, len(sources))
	for _, source := range sources {
		sourceByKey[source.Key] = source
	}
	logRoots := make(map[string]qingLongLogEntry, len(logs))
	for _, entry := range logs {
		logRoots[entry.Key] = entry
		if entry.Title != "" {
			logRoots[entry.Title] = entry
		}
	}
	out := make([]accountRunPublic, 0)
	for _, job := range jobs {
		cron, exists := cronsByID[job.QLCronID]
		if !exists {
			continue
		}
		rootKey := strings.Trim(cron.LogName, "/")
		if rootKey == "" {
			if separator := strings.Index(cron.LogPath, "/"); separator > 0 {
				rootKey = cron.LogPath[:separator]
			}
		}
		root, exists := logRoots[rootKey]
		if !exists {
			continue
		}
		children := append([]qingLongLogEntry(nil), root.Children...)
		sort.Slice(children, func(i, j int) bool { return children[i].CreateTime > children[j].CreateTime })
		name := job.ScriptKey
		if key, ok := matchScriptKey(sources, job.ScriptKey); ok {
			if source, found := sourceByKey[key]; found {
				name = source.displayName()
			}
		}
		for index, entry := range children {
			if entry.Type != "file" || !strings.HasSuffix(strings.ToLower(entry.Title), ".log") {
				continue
			}
			logKey := entry.Key
			if logKey == "" {
				logKey = strings.TrimRight(root.Key, "/") + "/" + entry.Title
			}
			running := cron.running() && index == 0
			status := "已完成"
			if running {
				status = "运行中"
			}
			out = append(out, accountRunPublic{
				AccountID: accountID, ScriptKey: job.ScriptKey, Name: name, QLCronID: cron.ID,
				LogKey: logKey, StartedAt: entry.CreateTime / 1000, Size: entry.Size, Running: running, TaskStatus: status,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	if len(out) > 100 {
		out = out[:100]
	}
	return out, nil
}

func (a *App) handleQingLongPush(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		acc, ok := a.resolveAccountFromQuery(w, r)
		if !ok {
			return
		}
		setting, err := a.db.AccountPushSettingOrDefault(r.Context(), acc.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		public, err := a.pushSettingPublic(r.Context(), setting)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, public)
	case http.MethodPut:
		var body pushSettingIn
		if err := decodeOptionalJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		acc, ok := a.resolveAccountRef(w, r, body.Ref)
		if !ok {
			return
		}
		setting, err := a.savePushSetting(r.Context(), acc, body)
		if err != nil {
			if errors.Is(err, errPushTokenRequired) {
				writeError(w, http.StatusBadRequest, err.Error())
			} else {
				writeError(w, http.StatusBadGateway, err.Error())
			}
			return
		}
		public, err := a.pushSettingPublic(r.Context(), setting)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, public)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// 脚本池的来源：脚本目录里的文件（主源），或从定时任务反推（降级）。
const (
	scriptSourceFiles = "scripts"
	scriptSourceCrons = "crons"
)

// scriptCatalogResult 是一次脚本池查询的全部结果。
type scriptCatalogResult struct {
	Sources   []scriptSource
	CronsByID map[int64]qingLongCron
	Total     int    // 数据源里的条目总数，供页面区分「列表为空」的不同原因
	Source    string // scripts（脚本目录）| crons（定时任务，降级）
	Degraded  string // 降级原因；主源可用时为空
}

// 「可用脚本」池 = 青龙脚本目录里的脚本文件。
//
// 用定时任务来推断「有哪些脚本」是不对的：定时任务回答的是「要跑什么」，
// 脚本文件才是「能跑什么」。混在一起会同时踩两个坑 —— 用户自己建的任务被当成
// 脚本，而网关给账号建的托管任务又得反过来排除；更糟的是同一个脚本在青龙里
// 已有全局任务时，再挂一个账号任务就会真的跑两遍。
//
// 所以现在：池子读脚本目录，定时任务只用来回答「这个账号挂了这个脚本没有」。
// 面板没有脚本接口时（老青龙、代代面板）才退回从定时任务反推，并在结果里标出来源，
// 页面会说明当前列表是从哪来的。
func (a *App) scriptCatalog(ctx context.Context) (scriptCatalogResult, error) {
	if !a.qinglong.configured() {
		return scriptCatalogResult{}, fmt.Errorf("面板 OpenAPI 未配置")
	}
	crons, err := a.qinglong.listCrons(ctx, "")
	if err != nil {
		return scriptCatalogResult{}, err
	}
	result := scriptCatalogResult{CronsByID: make(map[int64]qingLongCron, len(crons))}
	for _, cron := range crons {
		result.CronsByID[cron.ID] = cron
	}

	scripts, scriptErr := a.qinglong.listScripts(ctx)
	if scriptErr != nil {
		result.Sources = sourcesFromCrons(crons)
		result.Total = len(crons)
		result.Source = scriptSourceCrons
		result.Degraded = scriptErr.Error()
		return result, nil
	}
	result.Sources = mergeScriptSources(scripts, crons)
	result.Total = len(scripts)
	result.Source = scriptSourceFiles
	return result, nil
}

// 脚本目录里的文件 -> 脚本池。
//
// 同名的全局任务只用来补两样东西：调度（沿用用户已经调好的 cron）和「重复执行」提示；
// 一个脚本能不能挂给账号，只取决于它是不是脚本文件。
func mergeScriptSources(scripts []qingLongScript, crons []qingLongCron) []scriptSource {
	globalByPath := make(map[string]qingLongCron, len(crons))
	for _, cron := range crons {
		if strings.HasPrefix(cron.Name, managedTaskPrefix) {
			continue // 网关给账号建的托管任务，不是「全局任务」
		}
		path := scriptPathFromCommand(cron.Command)
		if path == "" {
			continue
		}
		if _, exists := globalByPath[path]; !exists {
			globalByPath[path] = cron
		}
	}

	seen := make(map[string]struct{}, len(scripts))
	out := make([]scriptSource, 0, len(scripts))
	for _, script := range scripts {
		if !validScriptPath(script.Path) || isIgnoredScriptPath(script.Path) {
			continue
		}
		if _, exists := seen[script.Path]; exists {
			continue
		}
		seen[script.Path] = struct{}{}
		source := scriptSource{
			Key:      script.Path,
			Name:     script.Name,
			Dir:      script.Dir,
			Schedule: defaultAccountTaskSchedule,
		}
		if cron, found := globalByPath[script.Path]; found {
			cron := cron
			source.GlobalCron = &cron
			if schedule := cron.getSchedule(); schedule != "" {
				source.Schedule = schedule
			}
		}
		out = append(out, source)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Key) < strings.ToLower(out[j].Key) })
	return out
}

// 降级路径：面板没有脚本接口时，从定时任务反推脚本（v7.1 及更早的行为）。
func sourcesFromCrons(crons []qingLongCron) []scriptSource {
	seen := make(map[string]struct{}, len(crons))
	out := make([]scriptSource, 0, len(crons))
	for _, cron := range crons {
		path, ok := parseScriptPathFromCron(cron)
		if !ok {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		script := newQingLongScript(path)
		out = append(out, scriptSource{
			Key:      path,
			Name:     script.Name,
			Dir:      script.Dir,
			Schedule: cron.getSchedule(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Key) < strings.ToLower(out[j].Key) })
	return out
}

// 把库里存的 script_key 归一到当前脚本池的写法。
//
// v7.1 及更早，脚本池的 key 是裸文件名（`绿鼻子.js`），现在改成完整相对路径
// （`code脚本/绿鼻子.js`）。历史数据仍按裸名存着，这里做一次兼容：精确匹配优先，
// 其次按文件名唯一匹配；匹配不上说明这个脚本已经不在池子里了。
func matchScriptKey(sources []scriptSource, raw string) (string, bool) {
	raw = strings.Trim(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", false
	}
	fallback, matches := "", 0
	for _, source := range sources {
		if source.Key == raw {
			return source.Key, true
		}
		if source.Name == raw {
			fallback, matches = source.Key, matches+1
		}
	}
	if matches == 1 {
		return fallback, true
	}
	return "", false
}

// 从定时任务的命令里解析出脚本路径（相对脚本根目录），认不出来返回 false。
//
// 只在降级路径、「找同名全局任务」这两处用到。返回完整相对路径而不是裸文件名：
// 不同目录下的同名脚本是两个脚本，混成一个会让账号任务挂错文件。
//
// 认得出这些常见写法：
//
//	task code脚本/绿鼻子.js                          -> code脚本/绿鼻子.js
//	task 525815266_YYB-Go-Enhanced/scripts/DTSH.py   -> 525815266_YYB-Go-Enhanced/scripts/DTSH.py
//	node /ql/scripts/美的会员.js                      -> 美的会员.js
//	task 绿鼻子.js                                    -> 绿鼻子.js
func parseScriptPathFromCron(cron qingLongCron) (string, bool) {
	// 网关自己给账号建的定时任务（名字带 [YYB:<账号ID>] 前缀）不算「可用脚本」
	if strings.HasPrefix(cron.Name, managedTaskPrefix) {
		return "", false
	}
	path := scriptPathFromCommand(cron.Command)
	if path == "" || !validScriptPath(path) || isIgnoredScriptPath(path) {
		return "", false
	}
	return path, true
}

// 脚本相对路径的合法性：字符集受限，且不能出现 . / .. 这类目录段。
// 这个路径会被拼进 `task <path>` 交给青龙执行，所以必须挡住穿越写法。
func validScriptPath(path string) bool {
	if !validScriptKey.MatchString(path) {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// 把定时任务的命令还原成「相对脚本目录的脚本路径」，认不出来就返回空串。
//
// 只取命令里的第一个参数：`task x.js now`、`node x.js > /dev/null` 这类写法后面的
// 内容都不是脚本路径。绝对路径（`/ql/scripts/x.js`）会切到脚本目录之后的部分。
func scriptPathFromCommand(command string) string {
	cmd := strings.TrimSpace(command)
	// 运行器前缀可能叠加，例如 `node task x.js`，所以循环剥离
	for {
		stripped := false
		for _, prefix := range []string{"task", "node", "python3", "python", "bash", "sh", "ts-node"} {
			if strings.HasPrefix(cmd, prefix+" ") {
				cmd = strings.TrimSpace(cmd[len(prefix):])
				stripped = true
				break
			}
		}
		if !stripped {
			break
		}
	}
	if fields := strings.Fields(cmd); len(fields) > 0 {
		cmd = fields[0]
	}
	cmd = strings.Trim(cmd, `"'`)
	cmd = strings.ReplaceAll(cmd, `\`, "/")
	if strings.HasPrefix(cmd, "/") {
		// 绝对路径：`/ql/scripts/...`、`/ql/data/scripts/...` 都取 /scripts/ 之后
		const marker = "/scripts/"
		idx := strings.LastIndex(cmd, marker)
		if idx == -1 {
			return ""
		}
		cmd = cmd[idx+len(marker):]
	}
	return strings.Trim(cmd, "/")
}

// 不能被挂给账号单独跑的文件。
//
// 两类：青龙自带的通知模块，以及我们自己的共用工具/批量执行器 ——
// 前者是给别人调用的库，后者一跑就会连带动所有账号，挂上去只会出错。
var ignoredScriptNames = map[string]struct{}{
	"SendNotify.py":   {},
	"notify.js":       {},
	"wechat_tools.js": {},
	"wechat_tools.py": {},
	"env.js":          {},
	"!RunAll.py":      {},
}

// 依赖与版本控制目录里的 js 不是业务脚本（青龙一般已排除，这里再挡一道）。
var ignoredScriptDirs = map[string]struct{}{
	"node_modules": {}, ".git": {}, ".github": {}, ".vscode": {},
	"backup": {}, "deps": {}, "__pycache__": {},
}

func isIgnoredScriptPath(path string) bool {
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return true
	}
	segments := strings.Split(path, "/")
	name := segments[len(segments)-1]
	for _, segment := range segments {
		if _, ignored := ignoredScriptDirs[segment]; ignored {
			return true
		}
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	if _, ignored := ignoredScriptNames[name]; ignored {
		return true
	}
	return strings.Contains(name, "SendNotify.py") || strings.Contains(name, "eoos_checkin.py")
}

func (a *App) ensureAccountJob(ctx context.Context, acc *store.WechatAccount, scriptKey string) (*store.AccountScriptJob, scriptSource, error) {
	catalog, err := a.scriptCatalog(ctx)
	if err != nil {
		return nil, scriptSource{}, err
	}
	// 页面传的是脚本池里的 key；历史数据可能是裸文件名，先归一化
	resolved, ok := matchScriptKey(catalog.Sources, scriptKey)
	if !ok {
		return nil, scriptSource{}, fmt.Errorf("不支持的脚本: %s", strings.TrimSpace(scriptKey))
	}
	source, found := findScriptSource(catalog.Sources, resolved)
	if !found {
		return nil, scriptSource{}, fmt.Errorf("不支持的脚本: %s", strings.TrimSpace(scriptKey))
	}
	scriptKey = resolved
	setting, err := a.db.AccountPushSettingOrDefault(ctx, acc.ID)
	if err != nil {
		return nil, scriptSource{}, err
	}
	command, taskBefore, err := a.accountTaskSpec(acc, scriptKey, setting)
	if err != nil {
		return nil, scriptSource{}, err
	}
	name := managedTaskName(acc, source.displayName())
	logName := managedLogName(acc.ID, scriptKey)
	job, err := a.db.GetAccountScriptJob(ctx, acc.ID, scriptKey)
	if err == nil {
		if _, exists := catalog.CronsByID[job.QLCronID]; exists {
			if err := a.qinglong.updateCron(ctx, job.QLCronID, name, command, source.Schedule, taskBefore, logName); err != nil {
				return nil, scriptSource{}, err
			}
			return job, source, nil
		}
		_ = a.db.DeleteAccountScriptJob(ctx, acc.ID, scriptKey)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, scriptSource{}, err
	}
	cron, err := a.qinglong.createCron(ctx, name, command, source.Schedule, taskBefore, logName)
	if err != nil {
		return nil, scriptSource{}, err
	}
	if err := a.qinglong.setCronsEnabled(ctx, []int64{cron.ID}, false); err != nil {
		return nil, scriptSource{}, err
	}
	job, err = a.db.UpsertAccountScriptJob(ctx, acc.ID, scriptKey, cron.ID, source.Schedule)
	return job, source, err
}

func managedTaskName(acc *store.WechatAccount, sourceName string) string {
	prefix := fmt.Sprintf("[YYB:%d]", acc.ID)
	if acc.Remark != nil && strings.TrimSpace(*acc.Remark) != "" {
		return fmt.Sprintf("%s %s · %s", prefix, strings.TrimSpace(*acc.Remark), sourceName)
	}
	return prefix + " " + sourceName
}

func managedLogName(accountID int64, scriptKey string) string {
	sum := sha256.Sum256([]byte(scriptKey))
	return fmt.Sprintf("yyb_account_%d_%x", accountID, sum[:6])
}

// accountTaskSpec 拼出「网关托管的账号任务」：命令负责跑脚本，任务前命令负责把这
// 一次运行限定在这个账号上。
//
// ⚠️ 隔离靠的是任务前命令把 WECHAT_OPENIDS 覆写成该账号的 openid：脚本池里的脚本按
// WECHAT_SERVER + WECHAT_OPENIDS 取账号，WECHAT_OPENIDS 留空或写 ALL 时会去网关
// /instances 枚举全部账号 —— 那正是「点运行却把所有账号跑了一遍」的原因。
// 只有网关建的任务会被限定；用户自己在青龙里建的任务不受影响，仍按全局
// WECHAT_OPENIDS（想跑全部账号就写 ALL）。
func (a *App) accountTaskSpec(acc *store.WechatAccount, scriptPath string, setting *store.AccountPushSetting) (string, string, error) {
	// scriptPath 就是脚本相对青龙脚本目录的完整路径（如 code脚本/绿鼻子.js），
	// 青龙的 task 命令认这个写法，不用再拼目录前缀。
	if !validScriptPath(scriptPath) {
		return "", "", fmt.Errorf("脚本路径不合法")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`).MatchString(a.cfg.QingLongServer) {
		return "", "", fmt.Errorf("YYB_QINGLONG_SERVER 格式不合法")
	}
	if strings.TrimSpace(acc.OpenID) == "" || strings.ContainsAny(acc.OpenID, "'\r\n\\") {
		return "", "", fmt.Errorf("账号 openid 含有无法安全写进任务的字符，无法生成只跑该账号的任务")
	}
	pushKey, pushPlusToken, pushPlusTopic, qywxKey := "''", "''", "''", "''"
	switch setting.Channel {
	case "serverchan":
		pushKey = envReference(setting.TokenEnvName)
	case "pushplus":
		pushPlusToken = envReference(setting.TokenEnvName)
		if setting.TopicEnvName != "" {
			pushPlusTopic = envReference(setting.TopicEnvName)
		}
	case "qywx":
		qywxKey = envReference(setting.TokenEnvName)
	}
	command := "task " + scriptPath
	// WECHAT_OPENIDS 是脚本池里脚本认的账号清单（见 code脚本/wechat_tools.*）：
	// 在这里写死成本账号，脚本就不会再去 /instances 枚举全部账号。
	taskBefore := fmt.Sprintf(
		"export YYB_SERVER='%s@%d'; export WECHAT_OPENIDS='%s'; export PUSH_KEY=%s; export PUSH_PLUS_TOKEN=%s; export PUSH_PLUS_USER=%s; export QYWX_KEY=%s",
		a.cfg.QingLongServer, acc.ID, acc.OpenID, pushKey, pushPlusToken, pushPlusTopic, qywxKey,
	)
	return command, taskBefore, nil
}

func envReference(name string) string {
	if !regexp.MustCompile(`^[A-Z0-9_]+$`).MatchString(name) {
		return "''"
	}
	return `"${` + name + `:-}"`
}

var errPushTokenRequired = errors.New("首次配置该推送渠道时必须填写 Token 或 Key")

func pushEnvNames(accountID int64) map[string][2]string {
	prefix := "YYB_RUN_ACCOUNT_" + strconv.FormatInt(accountID, 10) + "_"
	return map[string][2]string{
		"serverchan": {prefix + "SERVERCHAN_KEY", ""},
		"pushplus":   {prefix + "PUSHPLUS_TOKEN", prefix + "PUSHPLUS_TOPIC"},
		"qywx":       {prefix + "QYWX_KEY", ""},
	}
}

func (a *App) savePushSetting(ctx context.Context, acc *store.WechatAccount, body pushSettingIn) (*store.AccountPushSetting, error) {
	channel := strings.ToLower(strings.TrimSpace(body.Channel))
	if channel == "" {
		channel = "none"
	}
	names := pushEnvNames(acc.ID)
	if channel != "none" {
		if _, ok := names[channel]; !ok {
			return nil, fmt.Errorf("不支持的推送渠道")
		}
	}
	allNames := make([]string, 0, 4)
	for _, pair := range names {
		allNames = append(allNames, pair[0])
		if pair[1] != "" {
			allNames = append(allNames, pair[1])
		}
	}
	if channel == "none" {
		if err := a.qinglong.setNamedEnvsEnabled(ctx, allNames, false); err != nil {
			return nil, err
		}
		setting, err := a.db.UpsertAccountPushSetting(ctx, acc.ID, "none", "", "")
		if err != nil {
			return nil, err
		}
		if err := a.refreshAccountJobCommands(ctx, acc, setting); err != nil {
			return nil, err
		}
		return setting, nil
	}
	selected := names[channel]
	configured, err := a.namedEnvHasValue(ctx, selected[0])
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(body.Token)
	if token == "" && !configured {
		return nil, errPushTokenRequired
	}
	if token != "" {
		if err := a.qinglong.upsertEnv(ctx, selected[0], token, fmt.Sprintf("YYB Go 账号 %d %s 推送", acc.ID, channel)); err != nil {
			return nil, err
		}
	}
	if selected[1] != "" && body.Topic != nil {
		if err := a.qinglong.upsertEnv(ctx, selected[1], strings.TrimSpace(*body.Topic), fmt.Sprintf("YYB Go 账号 %d PushPlus 群组", acc.ID)); err != nil {
			return nil, err
		}
	}
	otherNames := make([]string, 0, len(allNames))
	for _, name := range allNames {
		if name != selected[0] && name != selected[1] {
			otherNames = append(otherNames, name)
		}
	}
	if err := a.qinglong.setNamedEnvsEnabled(ctx, otherNames, false); err != nil {
		return nil, err
	}
	selectedNames := []string{selected[0]}
	if selected[1] != "" {
		selectedNames = append(selectedNames, selected[1])
	}
	if err := a.qinglong.setNamedEnvsEnabled(ctx, selectedNames, true); err != nil {
		return nil, err
	}
	setting, err := a.db.UpsertAccountPushSetting(ctx, acc.ID, channel, selected[0], selected[1])
	if err != nil {
		return nil, err
	}
	if err := a.refreshAccountJobCommands(ctx, acc, setting); err != nil {
		return nil, err
	}
	return setting, nil
}

func (a *App) refreshAccountJobCommands(ctx context.Context, acc *store.WechatAccount, setting *store.AccountPushSetting) error {
	catalog, err := a.scriptCatalog(ctx)
	if err != nil {
		return err
	}
	jobs, err := a.db.ListAccountScriptJobs(ctx, acc.ID)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if _, cronExists := catalog.CronsByID[job.QLCronID]; !cronExists {
			continue
		}
		// 库里可能是旧写法的裸文件名，先归一到当前池子的 key 再拼命令
		key, ok := matchScriptKey(catalog.Sources, job.ScriptKey)
		if !ok {
			continue
		}
		source, found := findScriptSource(catalog.Sources, key)
		if !found {
			continue
		}
		command, taskBefore, err := a.accountTaskSpec(acc, key, setting)
		if err != nil {
			return err
		}
		if err := a.qinglong.updateCron(ctx, job.QLCronID, managedTaskName(acc, source.displayName()), command, source.Schedule, taskBefore, managedLogName(acc.ID, key)); err != nil {
			return err
		}
	}
	return nil
}

func findScriptSource(sources []scriptSource, key string) (scriptSource, bool) {
	for _, source := range sources {
		if source.Key == key {
			return source, true
		}
	}
	return scriptSource{}, false
}

func (a *App) pushSettingPublic(ctx context.Context, setting *store.AccountPushSetting) (pushSettingPublic, error) {
	out := pushSettingPublic{Channel: setting.Channel}
	if setting.Channel == "none" || setting.TokenEnvName == "" {
		return out, nil
	}
	var err error
	out.TokenConfigured, err = a.namedEnvHasValue(ctx, setting.TokenEnvName)
	if err != nil {
		return out, err
	}
	if setting.TopicEnvName != "" {
		out.TopicConfigured, err = a.namedEnvHasValue(ctx, setting.TopicEnvName)
	}
	return out, err
}

func (a *App) namedEnvHasValue(ctx context.Context, name string) (bool, error) {
	if name == "" {
		return false, nil
	}
	envs, err := a.qinglong.listEnvs(ctx, name)
	if err != nil {
		return false, err
	}
	for _, env := range envs {
		if env.Name == name && strings.TrimSpace(env.Value) != "" {
			return true, nil
		}
	}
	return false, nil
}

func writeRunError(w http.ResponseWriter, err error) {
	if strings.HasPrefix(err.Error(), "不支持的脚本") || strings.Contains(err.Error(), "格式不合法") {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeError(w, http.StatusBadGateway, err.Error())
}

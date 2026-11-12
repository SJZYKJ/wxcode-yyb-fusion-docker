package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	arcadiaResponseLimit = 16 << 20
	arcadiaLogRoot       = "/arcadia/log"
)

var arcadiaManagedLogPattern = regexp.MustCompile(`\byyb_account_[0-9]+_[a-f0-9]+\b`)

type arcadiaDriver struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type arcadiaEnv struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	Value       string `json:"value"`
	Description string `json:"description"`
	Enable      int    `json:"enable"`
}

type arcadiaCron struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Shell       string  `json:"shell"`
	Cron        string  `json:"cron"`
	Active      int     `json:"active"`
	LastRuntime string  `json:"last_runtime"`
	LastRunUse  float64 `json:"last_run_use"`
	IsRunning   bool    `json:"is_running"`
}

type arcadiaFile struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Type      string `json:"type"`
	UpdatedAt string `json:"updated_at"`
	CreatedAt string `json:"created_at"`
}

type arcadiaFileList struct {
	Children []arcadiaFile `json:"children"`
}

func newArcadiaDriver(baseURL, token string, timeout time.Duration) *arcadiaDriver {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &arcadiaDriver{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:      strings.TrimSpace(token),
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (d *arcadiaDriver) PanelType() string {
	return PanelTypeArcadia
}

func (d *arcadiaDriver) Status(ctx context.Context) error {
	_, err := d.ListEnvs(ctx, "YYB_SERVER")
	return err
}

func (d *arcadiaDriver) request(ctx context.Context, method, path string, body, out any) error {
	if d.baseURL == "" || d.token == "" {
		return fmt.Errorf("Arcadia OpenAPI 未配置")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("api-token", d.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("连接 Arcadia 面板失败: %w", err)
	}
	defer resp.Body.Close()
	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, arcadiaResponseLimit+1))
	if err != nil {
		return err
	}
	if len(rawBody) > arcadiaResponseLimit {
		return fmt.Errorf("Arcadia 面板响应超过 %d MB，请清理过旧日志后重试", arcadiaResponseLimit>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Arcadia 面板 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(rawBody)))
	}
	var envelope struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		return fmt.Errorf("解析 Arcadia 面板响应失败: %w", err)
	}
	if envelope.Code != 1 {
		message := strings.TrimSpace(envelope.Message)
		if message == "" {
			message = "未知错误"
		}
		return fmt.Errorf("Arcadia 面板返回错误 (%d): %s", envelope.Code, message)
	}
	if out == nil {
		return nil
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return fmt.Errorf("Arcadia 面板返回空数据")
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("解析 Arcadia 面板 result 失败: %w", err)
	}
	return nil
}

func (d *arcadiaDriver) ListEnvs(ctx context.Context, searchValue string) ([]qingLongEnv, error) {
	searchValue = strings.TrimSpace(searchValue)
	if searchValue == "" {
		searchValue = "YYB_SERVER"
	}
	path := "/api/open/env/v1/query?" + url.Values{"name": {searchValue}}.Encode()
	var records []struct {
		Category string     `json:"category"`
		Data     arcadiaEnv `json:"data"`
	}
	if err := d.request(ctx, http.MethodGet, path, nil, &records); err != nil {
		return nil, err
	}
	out := make([]qingLongEnv, 0, len(records))
	for _, record := range records {
		if record.Category != "ordinary" {
			continue
		}
		enabled := record.Data.Enable == 1
		out = append(out, qingLongEnv{
			ID: record.Data.ID, Name: record.Data.Type, Value: record.Data.Value,
			Remarks: record.Data.Description, Enabled: &enabled,
		})
	}
	return out, nil
}

func (d *arcadiaDriver) UpsertEnv(ctx context.Context, name, value, remarks string) error {
	envs, err := d.ListEnvs(ctx, name)
	if err != nil {
		return err
	}
	for _, env := range envs {
		if env.Name == name {
			return d.UpdateEnv(ctx, env.ID, name, value, remarks)
		}
	}
	body := map[string]any{
		"category": "ordinary",
		"data":     map[string]any{"type": name, "value": value, "description": remarks, "enable": 1},
	}
	return d.request(ctx, http.MethodPost, "/api/open/env/v1/create", body, nil)
}

func (d *arcadiaDriver) UpdateEnv(ctx context.Context, id int64, name, value, remarks string) error {
	body := map[string]any{
		"category": "ordinary",
		"data":     map[string]any{"id": id, "type": name, "value": value, "description": remarks},
	}
	return d.request(ctx, http.MethodPost, "/api/open/env/v1/update", body, nil)
}

func (d *arcadiaDriver) UpdateEnvEntry(ctx context.Context, env qingLongEnv, newValue string) error {
	return d.UpdateEnv(ctx, env.ID, env.Name, newValue, env.Remarks)
}

func (d *arcadiaDriver) DeleteEnvs(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return d.request(ctx, http.MethodPost, "/api/open/env/v1/delete", map[string]any{
		"id": ids, "isComposite": false,
	}, nil)
}

func (d *arcadiaDriver) SetEnvsEnabled(ctx context.Context, ids []int64, enabled bool) error {
	status := 0
	if enabled {
		status = 1
	}
	return d.request(ctx, http.MethodPost, "/api/open/env/v1/changeStatus", map[string]any{
		"id": ids, "status": status, "isComposite": false,
	}, nil)
}

func (d *arcadiaDriver) SetNamedEnvsEnabled(ctx context.Context, names []string, enabled bool) error {
	for _, name := range names {
		envs, err := d.ListEnvs(ctx, name)
		if err != nil {
			return err
		}
		var ids []int64
		for _, env := range envs {
			if env.Name == name {
				ids = append(ids, env.ID)
			}
		}
		if len(ids) > 0 {
			if err := d.SetEnvsEnabled(ctx, ids, enabled); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *arcadiaDriver) ListCrons(ctx context.Context, search string) ([]qingLongCron, error) {
	values := url.Values{"page": {"1"}, "size": {"1000"}}
	if strings.TrimSpace(search) != "" {
		values.Set("search", strings.TrimSpace(search))
	}
	var page struct {
		Data  []arcadiaCron `json:"data"`
		Total int           `json:"total"`
	}
	if err := d.request(ctx, http.MethodGet, "/api/open/cron/v1/page?"+values.Encode(), nil, &page); err != nil {
		return nil, err
	}
	out := make([]qingLongCron, 0, len(page.Data))
	for _, item := range page.Data {
		enabled := item.Active == 1
		running := 0
		if item.IsRunning {
			running = 1
		}
		cron := qingLongCron{
			ID: item.ID, Name: item.Name, Command: item.Shell, Schedule: item.Cron,
			Enabled: &enabled, IsRunning: &running, LastExecutionTime: item.LastRuntime,
			LastRunningTime: item.LastRunUse,
		}
		if match := arcadiaManagedLogPattern.FindString(item.Shell); match != "" {
			cron.LogName = match
			cron.LogPath = arcadiaLogRoot + "/" + match
		}
		out = append(out, cron)
	}
	return out, nil
}

func arcadiaRunCommand(command string, noNativeLog bool) string {
	command = strings.TrimSpace(command)
	if strings.HasPrefix(command, "task ") {
		run := "arcadia run " + strings.TrimSpace(strings.TrimPrefix(command, "task "))
		if noNativeLog {
			run += " --no-log"
		}
		return run
	}
	return command
}

func arcadiaManagedShell(command, taskBefore, logName string) string {
	run := arcadiaRunCommand(command, logName != "")
	if logName == "" {
		if taskBefore == "" {
			return run
		}
		return taskBefore + "; " + run
	}
	dir := arcadiaLogRoot + "/" + logName
	body := run
	if strings.TrimSpace(taskBefore) != "" {
		body = taskBefore + "; " + run
	}
	return fmt.Sprintf("YYB_LOG_FILE=\"%s/$(date '+%%Y-%%m-%%d-%%H-%%M-%%S')-$$.log\"; mkdir -p \"%s\"; { %s; } >\"$YYB_LOG_FILE\" 2>&1", dir, dir, body)
}

func (d *arcadiaDriver) CreateCron(ctx context.Context, name, command, schedule, taskBefore, logName string) (*qingLongCron, error) {
	body := map[string]any{
		"name": name, "cron": schedule, "shell": arcadiaManagedShell(command, taskBefore, logName),
		"active": 0, "remark": "wxcode-YYB 账号独立任务",
	}
	var created arcadiaCron
	if err := d.request(ctx, http.MethodPost, "/api/open/cron/v1/create", body, &created); err != nil {
		return nil, err
	}
	if created.ID == 0 {
		return nil, fmt.Errorf("Arcadia 面板创建任务后未返回任务 ID")
	}
	enabled := created.Active == 1
	return &qingLongCron{ID: created.ID, Name: created.Name, Command: created.Shell, Schedule: created.Cron, Enabled: &enabled, LogName: logName}, nil
}

func (d *arcadiaDriver) UpdateCron(ctx context.Context, id int64, name, command, schedule, taskBefore, logName string) error {
	body := map[string]any{
		"id": id, "name": name, "cron": schedule,
		"shell": arcadiaManagedShell(command, taskBefore, logName), "remark": "wxcode-YYB 账号独立任务",
	}
	return d.request(ctx, http.MethodPost, "/api/open/cron/v1/update", body, nil)
}

func (d *arcadiaDriver) SetCronsEnabled(ctx context.Context, ids []int64, enabled bool) error {
	active := 0
	if enabled {
		active = 1
	}
	for _, id := range ids {
		if err := d.request(ctx, http.MethodPost, "/api/open/cron/v1/update", map[string]any{"id": id, "active": active}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (d *arcadiaDriver) RunCrons(ctx context.Context, ids []int64) error {
	return d.request(ctx, http.MethodPost, "/api/open/cron/v1/run", map[string]any{"id": ids}, nil)
}

func (d *arcadiaDriver) DeleteCrons(ctx context.Context, ids []int64) error {
	return d.request(ctx, http.MethodPost, "/api/open/cron/v1/delete", map[string]any{"id": ids}, nil)
}

func (d *arcadiaDriver) CronLog(ctx context.Context, id int64) (string, error) {
	crons, err := d.ListCrons(ctx, "")
	if err != nil {
		return "", err
	}
	for _, cron := range crons {
		if cron.ID != id {
			continue
		}
		if cron.LogName == "" {
			return "", fmt.Errorf("Arcadia 任务没有 YYB 托管日志目录")
		}
		files, err := d.listLogFiles(ctx, cron.LogName)
		if err != nil {
			return "", err
		}
		if len(files) == 0 {
			return "", nil
		}
		return d.LogDetail(ctx, cron.LogName, files[0].Name)
	}
	return "", fmt.Errorf("Arcadia 任务 %d 不存在", id)
}

func (d *arcadiaDriver) fileList(ctx context.Context, path string) ([]arcadiaFile, error) {
	var result arcadiaFileList
	query := url.Values{"path": {path}}
	if err := d.request(ctx, http.MethodGet, "/api/open/file/v1/list?"+query.Encode(), nil, &result); err != nil {
		return nil, err
	}
	return result.Children, nil
}

func (d *arcadiaDriver) listLogFiles(ctx context.Context, dir string) ([]arcadiaFile, error) {
	if !arcadiaManagedLogPattern.MatchString(dir) || arcadiaManagedLogPattern.FindString(dir) != dir {
		return nil, fmt.Errorf("无效的 Arcadia 日志目录: %s", dir)
	}
	files, err := d.fileList(ctx, arcadiaLogRoot+"/"+dir)
	if err != nil {
		return nil, err
	}
	filtered := files[:0]
	for _, file := range files {
		if file.Type == "file" && !strings.ContainsAny(file.Name, `/\\`) {
			filtered = append(filtered, file)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].UpdatedAt == filtered[j].UpdatedAt {
			return filtered[i].Name > filtered[j].Name
		}
		return filtered[i].UpdatedAt > filtered[j].UpdatedAt
	})
	return filtered, nil
}

func (d *arcadiaDriver) ListLogs(ctx context.Context) ([]qingLongLogEntry, error) {
	entries, err := d.fileList(ctx, arcadiaLogRoot)
	if err != nil {
		return nil, err
	}
	out := make([]qingLongLogEntry, 0)
	for _, entry := range entries {
		if entry.Type != "folder" || arcadiaManagedLogPattern.FindString(entry.Name) != entry.Name {
			continue
		}
		files, err := d.listLogFiles(ctx, entry.Name)
		if err != nil {
			return nil, fmt.Errorf("读取 Arcadia 日志目录 %s 失败: %w", entry.Name, err)
		}
		children := make([]qingLongLogEntry, 0, len(files))
		for _, file := range files {
			children = append(children, qingLongLogEntry{
				Title: file.Name, Key: entry.Name + "/" + file.Name, Type: "file", Parent: entry.Name,
				CreateTime: arcadiaLogTime(file),
			})
		}
		out = append(out, qingLongLogEntry{Title: entry.Name, Key: entry.Name, Type: "directory", Children: children})
	}
	return out, nil
}

func arcadiaFileTime(value string) int64 {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}
	// qingLongLogEntry.CreateTime uses milliseconds for all panel drivers.
	return parsed.UnixMilli()
}

func arcadiaLogTime(file arcadiaFile) int64 {
	for _, value := range []string{file.CreatedAt, file.UpdatedAt} {
		if timestamp := arcadiaFileTime(value); timestamp != 0 {
			return timestamp
		}
	}
	name := strings.TrimSuffix(file.Name, ".log")
	const layout = "2006-01-02-15-04-05"
	if len(name) < len(layout) {
		return 0
	}
	parsed, err := time.ParseInLocation(layout, name[:len(layout)], time.Local)
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}

func (d *arcadiaDriver) LogDetail(ctx context.Context, dir, filename string) (string, error) {
	if arcadiaManagedLogPattern.FindString(dir) != dir || filename == "" || strings.ContainsAny(filename, `/\\`) {
		return "", fmt.Errorf("无效的 Arcadia 日志路径")
	}
	var content string
	query := url.Values{"path": {arcadiaLogRoot + "/" + dir + "/" + filename}}
	if err := d.request(ctx, http.MethodGet, "/api/open/file/v1/content?"+query.Encode(), nil, &content); err != nil {
		if strings.Contains(err.Error(), "权限不足") {
			return "", fmt.Errorf("Arcadia Token 缺少 file:read 权限: %w", err)
		}
		return "", err
	}
	return content, nil
}

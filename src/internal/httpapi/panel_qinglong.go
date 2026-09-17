package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type qingLongDriver struct {
	baseURL      string
	clientID     string
	clientSecret string
	httpClient   *http.Client
	mu           sync.Mutex
	token        string
	tokenExpiry  time.Time
}

const qingLongResponseLimit = 16 << 20

func newQingLongDriver(baseURL, clientID, clientSecret string, timeout time.Duration) *qingLongDriver {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &qingLongDriver{
		baseURL:      strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		clientID:     strings.TrimSpace(clientID),
		clientSecret: strings.TrimSpace(clientSecret),
		httpClient:   &http.Client{Timeout: timeout},
	}
}

func (d *qingLongDriver) PanelType() string {
	return PanelTypeQingLong
}

func (d *qingLongDriver) Status(ctx context.Context) error {
	_, err := d.authenticate(ctx)
	return err
}

func (d *qingLongDriver) authenticate(ctx context.Context) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.baseURL == "" || d.clientID == "" || d.clientSecret == "" {
		return "", fmt.Errorf("青龙面板 OpenAPI 未配置")
	}
	if d.token != "" && time.Now().Before(d.tokenExpiry) {
		return d.token, nil
	}

	query := url.Values{"client_id": {d.clientID}, "client_secret": {d.clientSecret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.baseURL+"/open/auth/token?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("连接青龙面板失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("青龙面板鉴权返回 HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Token      string `json:"token"`
			Expiration int64  `json:"expiration"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("解析青龙面板鉴权响应失败: %w", err)
	}
	if envelope.Code != 0 && envelope.Code != 200 {
		return "", fmt.Errorf("青龙面板鉴权失败，状态码 %d", envelope.Code)
	}
	if envelope.Data.Token == "" {
		return "", fmt.Errorf("青龙面板鉴权未返回 token")
	}
	ttl := time.Duration(envelope.Data.Expiration) * time.Second
	if ttl <= time.Minute {
		ttl = 10 * time.Minute
	}
	d.token = envelope.Data.Token
	d.tokenExpiry = time.Now().Add(ttl - time.Minute)
	return d.token, nil
}

func (d *qingLongDriver) request(ctx context.Context, method, path string, body any, out any) error {
	token, err := d.authenticate(ctx)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, d.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, qingLongResponseLimit+1))
	if err != nil {
		return err
	}
	if len(rawBody) > qingLongResponseLimit {
		return fmt.Errorf("青龙面板响应超过 %d MB，请清理过旧日志后重试", qingLongResponseLimit>>20)
	}
	if len(rawBody) == 0 && out != nil {
		return fmt.Errorf("青龙面板返回空响应 (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("青龙面板 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(rawBody)))
	}
	if out == nil {
		return nil
	}
	var envelope struct {
		Code int             `json:"code"`
		Msg  string          `json:"message"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &envelope); err == nil && (envelope.Code == 200 || envelope.Code == 0) && envelope.Data != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("解析青龙面板响应 data 失败: %w", err)
		}
		return nil
	}
	if err := json.Unmarshal(rawBody, out); err != nil {
		return fmt.Errorf("解析青龙面板响应失败: %w", err)
	}
	return nil
}

func (d *qingLongDriver) ListEnvs(ctx context.Context, searchValue string) ([]qingLongEnv, error) {
	path := "/open/envs?" + url.Values{"searchValue": {searchValue}}.Encode()
	var list []qingLongEnv
	if err := d.request(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func (d *qingLongDriver) UpsertEnv(ctx context.Context, name, value, remarks string) error {
	envs, err := d.ListEnvs(ctx, name)
	if err != nil {
		return err
	}
	for _, env := range envs {
		if env.Name == name {
			return d.UpdateEnv(ctx, env.ID, name, value, remarks)
		}
	}
	body := []map[string]string{{"name": name, "value": value, "remarks": remarks}}
	return d.request(ctx, http.MethodPost, "/open/envs", body, nil)
}

func (d *qingLongDriver) UpdateEnv(ctx context.Context, id int64, name, value, remarks string) error {
	body := map[string]any{"id": id, "name": name, "value": value, "remarks": remarks}
	return d.request(ctx, http.MethodPut, "/open/envs", body, nil)
}

func (d *qingLongDriver) UpdateEnvEntry(ctx context.Context, env qingLongEnv, newValue string) error {
	return d.UpdateEnv(ctx, env.ID, env.Name, newValue, env.Remarks)
}

func (d *qingLongDriver) SetEnvsEnabled(ctx context.Context, ids []int64, enabled bool) error {
	path := "/open/envs/disable"
	if enabled {
		path = "/open/envs/enable"
	}
	return d.request(ctx, http.MethodPut, path, ids, nil)
}

func (d *qingLongDriver) SetNamedEnvsEnabled(ctx context.Context, names []string, enabled bool) error {
	if len(names) == 0 {
		return nil
	}
	nameMap := make(map[string]bool, len(names))
	for _, n := range names {
		nameMap[n] = true
	}
	envs, err := d.ListEnvs(ctx, "")
	if err != nil {
		return err
	}
	var ids []int64
	for _, env := range envs {
		if nameMap[env.Name] {
			ids = append(ids, env.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return d.SetEnvsEnabled(ctx, ids, enabled)
}

func (d *qingLongDriver) ListCrons(ctx context.Context, search string) ([]qingLongCron, error) {
	path := "/open/crons?" + url.Values{"searchValue": {search}}.Encode()
	var raw json.RawMessage
	if err := d.request(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return []qingLongCron{}, nil
	}
	var out []qingLongCron
	if err := json.Unmarshal(raw, &out); err == nil {
		return out, nil
	}
	var page struct {
		Data []qingLongCron `json:"data"`
		List []qingLongCron `json:"list"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, err
	}
	if page.Data != nil {
		return page.Data, nil
	}
	return page.List, nil
}

func (d *qingLongDriver) CreateCron(ctx context.Context, name, command, schedule, taskBefore, logName string) (*qingLongCron, error) {
	body := map[string]any{
		"name":        name,
		"command":     command,
		"schedule":    schedule,
		"task_before": taskBefore,
		"log_name":    logName,
	}
	var raw json.RawMessage
	if err := d.request(ctx, http.MethodPost, "/open/crons", body, &raw); err != nil {
		return nil, err
	}
	var cron qingLongCron
	if err := json.Unmarshal(raw, &cron); err == nil && cron.ID != 0 {
		return &cron, nil
	}
	var list []qingLongCron
	if err := json.Unmarshal(raw, &list); err == nil && len(list) > 0 {
		return &list[0], nil
	}
	return nil, fmt.Errorf("青龙面板创建任务后未返回任务 ID")
}

func (d *qingLongDriver) UpdateCron(ctx context.Context, id int64, name, command, schedule, taskBefore, logName string) error {
	body := map[string]any{"id": id, "name": name, "command": command, "schedule": schedule, "task_before": taskBefore, "log_name": logName}
	return d.request(ctx, http.MethodPut, "/open/crons", body, nil)
}

func (d *qingLongDriver) SetCronsEnabled(ctx context.Context, ids []int64, enabled bool) error {
	path := "/open/crons/disable"
	if enabled {
		path = "/open/crons/enable"
	}
	return d.request(ctx, http.MethodPut, path, ids, nil)
}

func (d *qingLongDriver) RunCrons(ctx context.Context, ids []int64) error {
	return d.request(ctx, http.MethodPut, "/open/crons/run", ids, nil)
}

func (d *qingLongDriver) DeleteCrons(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return d.request(ctx, http.MethodDelete, "/open/crons", ids, nil)
}

func (d *qingLongDriver) CronLog(ctx context.Context, id int64) (string, error) {
	var out string
	if err := d.request(ctx, http.MethodGet, fmt.Sprintf("/open/crons/%d/log", id), nil, &out); err != nil {
		return "", err
	}
	return out, nil
}

func (d *qingLongDriver) ListLogs(ctx context.Context) ([]qingLongLogEntry, error) {
	var list []qingLongLogEntry
	if err := d.request(ctx, http.MethodGet, "/open/logs", nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func (d *qingLongDriver) LogDetail(ctx context.Context, dir, filename string) (string, error) {
	logPath := dir
	if filename != "" {
		if logPath != "" {
			logPath += "/" + filename
		} else {
			logPath = filename
		}
	}
	var out string
	reqPath := "/open/logs/detail?" + url.Values{"path": {logPath}}.Encode()
	if err := d.request(ctx, http.MethodGet, reqPath, nil, &out); err == nil && out != "" {
		return out, nil
	}
	altPath := "/open/logs/" + url.PathEscape(dir)
	if filename != "" {
		altPath += "/" + url.PathEscape(filename)
	}
	if err := d.request(ctx, http.MethodGet, altPath, nil, &out); err == nil {
		return out, nil
	}
	return "", d.request(ctx, http.MethodGet, reqPath, nil, &out)
}

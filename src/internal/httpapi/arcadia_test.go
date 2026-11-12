package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeArcadiaPanel struct {
	mu      sync.Mutex
	token   string
	envs    []arcadiaEnv
	crons   []arcadiaCron
	runIDs  []int64
	deleted []int64
}

func arcadiaTestResponse(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": "success", "result": result})
}

func newFakeArcadiaPanel(t *testing.T) (*fakeArcadiaPanel, *httptest.Server) {
	t.Helper()
	fake := &fakeArcadiaPanel{
		token: "arcadia-test-token",
		envs:  []arcadiaEnv{{ID: 1, Type: "YYB_SERVER", Value: "yyb-go:8000@1", Description: "账号", Enable: 1}},
		crons: []arcadiaCron{{
			ID: 11, Name: "美的会员", Shell: "arcadia run SuperNaiBA_YYB-GO-Script/MDHY.js",
			Cron: "0 8 * * *", Active: 1, LastRuntime: "2026-08-20T01:02:03.123Z", LastRunUse: 4.5,
		}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-token") != fake.token {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 4403, "message": "认证失败"})
			return
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		switch {
		case r.URL.Path == "/api/open/env/v1/query" && r.Method == http.MethodGet:
			name := r.URL.Query().Get("name")
			var result []map[string]any
			for _, env := range fake.envs {
				if strings.Contains(env.Type, name) {
					result = append(result, map[string]any{"category": "ordinary", "data": env})
				}
			}
			arcadiaTestResponse(w, result)
		case r.URL.Path == "/api/open/env/v1/create" && r.Method == http.MethodPost:
			var body struct {
				Data arcadiaEnv `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			body.Data.ID = int64(len(fake.envs) + 1)
			fake.envs = append(fake.envs, body.Data)
			arcadiaTestResponse(w, body.Data)
		case r.URL.Path == "/api/open/env/v1/update" && r.Method == http.MethodPost:
			var body struct {
				Data arcadiaEnv `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for i := range fake.envs {
				if fake.envs[i].ID == body.Data.ID {
					body.Data.Enable = fake.envs[i].Enable
					fake.envs[i] = body.Data
				}
			}
			arcadiaTestResponse(w, body.Data)
		case r.URL.Path == "/api/open/env/v1/changeStatus" && r.Method == http.MethodPost:
			var body struct {
				IDs    []int64 `json:"id"`
				Status int     `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for i := range fake.envs {
				for _, id := range body.IDs {
					if fake.envs[i].ID == id {
						fake.envs[i].Enable = body.Status
					}
				}
			}
			arcadiaTestResponse(w, true)
		case r.URL.Path == "/api/open/cron/v1/page" && r.Method == http.MethodGet:
			search := r.URL.Query().Get("search")
			var data []arcadiaCron
			for _, cron := range fake.crons {
				if search == "" || strings.Contains(cron.Name, search) || strings.Contains(cron.Shell, search) {
					data = append(data, cron)
				}
			}
			arcadiaTestResponse(w, map[string]any{"data": data, "total": len(data), "page": 1, "size": 1000})
		case r.URL.Path == "/api/open/cron/v1/create" && r.Method == http.MethodPost:
			var cron arcadiaCron
			_ = json.NewDecoder(r.Body).Decode(&cron)
			cron.ID = int64(100 + len(fake.crons))
			fake.crons = append(fake.crons, cron)
			arcadiaTestResponse(w, cron)
		case r.URL.Path == "/api/open/cron/v1/update" && r.Method == http.MethodPost:
			var raw map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&raw)
			var id int64
			_ = json.Unmarshal(raw["id"], &id)
			for i := range fake.crons {
				if fake.crons[i].ID != id {
					continue
				}
				if value, ok := raw["name"]; ok {
					_ = json.Unmarshal(value, &fake.crons[i].Name)
				}
				if value, ok := raw["shell"]; ok {
					_ = json.Unmarshal(value, &fake.crons[i].Shell)
				}
				if value, ok := raw["cron"]; ok {
					_ = json.Unmarshal(value, &fake.crons[i].Cron)
				}
				if value, ok := raw["active"]; ok {
					_ = json.Unmarshal(value, &fake.crons[i].Active)
				}
			}
			arcadiaTestResponse(w, true)
		case r.URL.Path == "/api/open/cron/v1/run" && r.Method == http.MethodPost:
			var body struct {
				IDs []int64 `json:"id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			fake.runIDs = append(fake.runIDs, body.IDs...)
			arcadiaTestResponse(w, true)
		case r.URL.Path == "/api/open/cron/v1/delete" && r.Method == http.MethodPost:
			var body struct {
				IDs []int64 `json:"id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			fake.deleted = append(fake.deleted, body.IDs...)
			arcadiaTestResponse(w, true)
		case r.URL.Path == "/api/open/file/v1/list" && r.Method == http.MethodGet:
			path := r.URL.Query().Get("path")
			if path == arcadiaLogRoot {
				arcadiaTestResponse(w, map[string]any{"children": []arcadiaFile{{Name: "yyb_account_1_abcdef123456", Type: "folder"}}})
			} else {
				arcadiaTestResponse(w, map[string]any{"children": []arcadiaFile{
					{Name: "2026-08-20-01.log", Type: "file", UpdatedAt: "2026-08-20T01:00:00Z", CreatedAt: "2026-08-20T01:00:00Z"},
					{Name: "2026-08-20-02.log", Type: "file", UpdatedAt: "2026-08-20T02:00:00Z", CreatedAt: "2026-08-20T02:00:00Z"},
				}})
			}
		case r.URL.Path == "/api/open/file/v1/content" && r.Method == http.MethodGet:
			arcadiaTestResponse(w, "Arcadia managed log\n")
		default:
			t.Fatalf("unexpected Arcadia request: %s %s", r.Method, r.URL.String())
		}
	}))
	return fake, server
}

func TestArcadiaPanelDriver(t *testing.T) {
	fake, server := newFakeArcadiaPanel(t)
	defer server.Close()
	client := newQingLongClient(PanelTypeArcadia, server.URL, "api-token", fake.token, 5*time.Second)
	ctx := context.Background()

	if err := client.status(ctx); err != nil {
		t.Fatalf("status() error = %v", err)
	}
	if err := client.upsertEnv(ctx, "YYB_SERVER", "yyb-go:8000@1\nyyb-go:8000@2", "账号列表"); err != nil {
		t.Fatalf("upsertEnv() error = %v", err)
	}
	envs, err := client.listEnvs(ctx, "YYB_SERVER")
	if err != nil || len(envs) != 1 || envs[0].Value != "yyb-go:8000@1\nyyb-go:8000@2" {
		t.Fatalf("listEnvs() = %+v, %v", envs, err)
	}
	if err := client.setEnvsEnabled(ctx, []int64{1}, false); err != nil {
		t.Fatalf("setEnvsEnabled() error = %v", err)
	}

	crons, err := client.listCrons(ctx, "")
	if err != nil || len(crons) != 1 || crons[0].getLastExecutionAt() != 1787187723 {
		t.Fatalf("listCrons() = %+v, %v", crons, err)
	}
	key, repo, ok := parseScriptKeyFromCron(crons[0], []string{"SuperNaiBA_YYB-GO-Script"})
	if !ok || key != "MDHY.js" || repo != "SuperNaiBA_YYB-GO-Script" {
		t.Fatalf("parseScriptKeyFromCron() = %q, %q, %v", key, repo, ok)
	}

	logName := "yyb_account_1_abcdef123456"
	created, err := client.createCron(ctx, "[YYB:1] 美的会员", "task SuperNaiBA_YYB-GO-Script/MDHY.js", "0 8 * * *", "export YYB_SERVER='yyb-go:8000@1'", logName)
	if err != nil {
		t.Fatalf("createCron() error = %v", err)
	}
	fake.mu.Lock()
	managed := fake.crons[len(fake.crons)-1]
	fake.mu.Unlock()
	if managed.Active != 0 || !strings.Contains(managed.Shell, "arcadia run SuperNaiBA_YYB-GO-Script/MDHY.js --no-log") || !strings.Contains(managed.Shell, arcadiaLogRoot+"/"+logName) {
		t.Fatalf("managed Arcadia cron = %+v", managed)
	}
	if err := client.setCronsEnabled(ctx, []int64{created.ID}, true); err != nil {
		t.Fatalf("setCronsEnabled() error = %v", err)
	}
	if err := client.runCrons(ctx, []int64{created.ID}); err != nil {
		t.Fatalf("runCrons() error = %v", err)
	}
	logText, err := client.cronLog(ctx, created.ID)
	if err != nil || logText != "Arcadia managed log\n" {
		t.Fatalf("cronLog() = %q, %v", logText, err)
	}
	logs, err := client.listLogs(ctx)
	if err != nil || len(logs) != 1 || len(logs[0].Children) != 2 || !strings.HasSuffix(logs[0].Children[0].Key, "02.log") {
		t.Fatalf("listLogs() = %+v, %v", logs, err)
	}
	if err := client.deleteCrons(ctx, []int64{created.ID}); err != nil {
		t.Fatalf("deleteCrons() error = %v", err)
	}
}

func TestArcadiaBusinessError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 4405, "message": "权限不足"})
	}))
	defer server.Close()
	driver := newArcadiaDriver(server.URL, "token", time.Second)
	err := driver.Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "权限不足") {
		t.Fatalf("Status() error = %v", err)
	}
}

func TestArcadiaLogDetailExplainsFileReadPermission(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 4405, "message": "权限不足"})
	}))
	defer server.Close()
	driver := newArcadiaDriver(server.URL, "token", time.Second)
	_, err := driver.LogDetail(context.Background(), "yyb_account_1_abcdef", "2026-08-23-11-00-39.log")
	if err == nil || !strings.Contains(err.Error(), "file:read") {
		t.Fatalf("LogDetail() error = %v", err)
	}
}

func TestArcadiaFileTimeUsesMilliseconds(t *testing.T) {
	got := arcadiaFileTime("2026-08-20T01:02:03.123Z")
	want := int64(1787187723123)
	if got != want {
		t.Fatalf("arcadiaFileTime() = %d, want %d", got, want)
	}
}

func TestArcadiaLogTimeFallsBackToFileName(t *testing.T) {
	file := arcadiaFile{Name: "2026-08-23-11-00-39-3693.log"}
	want := time.Date(2026, 8, 23, 11, 0, 39, 0, time.Local).UnixMilli()
	if got := arcadiaLogTime(file); got != want {
		t.Fatalf("arcadiaLogTime() = %d, want %d", got, want)
	}
}

func TestArcadiaManagedShellKeepsSingleLog(t *testing.T) {
	managed := arcadiaManagedShell("task repo/demo.py", "export YYB_SERVER='yyb-go:8000@1'", "yyb_account_1_abcdef")
	if !strings.Contains(managed, "arcadia run repo/demo.py --no-log") {
		t.Fatalf("managed shell does not disable Arcadia native log: %s", managed)
	}
	if strings.Count(managed, ">\"$YYB_LOG_FILE\"") != 1 {
		t.Fatalf("managed shell should redirect to exactly one YYB log: %s", managed)
	}
	native := arcadiaManagedShell("task repo/demo.py", "", "")
	if strings.Contains(native, "--no-log") {
		t.Fatalf("unmanaged Arcadia command unexpectedly disabled native log: %s", native)
	}
}

func TestArcadiaExplicitTypeDoesNotAutoSwitch(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client := newQingLongClient(PanelTypeArcadia, server.URL, "api-token", "token", time.Second)
	if err := client.status(context.Background()); err == nil {
		t.Fatal("status() unexpectedly succeeded")
	}
	if got := client.getPanelType(); got != PanelTypeArcadia {
		t.Fatalf("panel type switched to %q", got)
	}
}

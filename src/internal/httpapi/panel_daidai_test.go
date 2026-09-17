package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDaidaiDriver_EnabledMapping(t *testing.T) {
	enabled := true
	disabled := false

	env1 := qingLongEnv{Name: "E1", Status: 0, Enabled: &enabled}
	if !env1.enabled() {
		t.Errorf("expected env1 to be enabled")
	}

	env2 := qingLongEnv{Name: "E2", Status: 0, Enabled: &disabled}
	if env2.enabled() {
		t.Errorf("expected env2 to be disabled, even though Status=0")
	}

	env3 := qingLongEnv{Name: "E3", Status: 1, Enabled: nil}
	if env3.enabled() {
		t.Errorf("expected env3 to be disabled based on Status=1")
	}

	env4 := qingLongEnv{Name: "E4", Status: 0, Enabled: nil}
	if !env4.enabled() {
		t.Errorf("expected env4 to be enabled based on Status=0")
	}
}

func TestQingLongCronStateTakesPriorityOverStalePID(t *testing.T) {
	enabled := 0
	idle := qingLongCron{Status: float64(1), IsDisabled: &enabled, PID: float64(1234)}
	if !idle.enabled() {
		t.Fatal("expected QingLong cron with isDisabled=0 to be enabled")
	}
	if idle.running() {
		t.Fatal("expected QingLong cron with status=1 to be idle even when PID is retained")
	}

	running := qingLongCron{Status: float64(0), IsDisabled: &enabled, PID: float64(1234)}
	if !running.running() {
		t.Fatal("expected QingLong cron with status=0 to be running")
	}
}

func TestDaidaiDriver_MockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/open-api/token":
			if r.Method != http.MethodPost {
				t.Errorf("expected POST for token, got %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"access_token": "mock-token-123",
					"token_type":   "Bearer",
					"expires_in":   86400,
				},
			})
		case "/api/v1/envs":
			if r.Method != http.MethodGet {
				t.Errorf("expected GET for envs, got %s", r.Method)
			}
			if r.URL.Query().Get("all") != "1" {
				t.Errorf("expected all=1 query param for envs list")
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": 1, "name": "JD_COOKIE", "value": "pt_key=1", "remarks": "acc1", "enabled": true},
					{"id": 2, "name": "JD_COOKIE", "value": "pt_key=2", "remarks": "acc2", "enabled": false},
				},
				"total":     2,
				"page":      1,
				"page_size": 2,
			})
		case "/api/v1/tasks":
			if r.Method != http.MethodGet {
				t.Errorf("expected GET for tasks, got %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": 10, "name": "task1", "command": "node task.js", "cron_expression": "0 0 * * *", "status": 1.0, "last_running_time": 1.5, "last_run_at": "2026-08-12T16:00:00Z"},
					{"id": 20, "name": "task2", "command": "python task.py", "cron_expression": "0 1 * * *", "status": 0.0, "last_running_time": 0.0},
				},
				"total":     2,
				"page":      1,
				"page_size": 2,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	driver := newDaidaiDriver(server.URL, "key", "secret", 5*time.Second)

	ctx := context.Background()
	envs, err := driver.ListEnvs(ctx, "")
	if err != nil {
		t.Fatalf("ListEnvs failed: %v", err)
	}
	if len(envs) != 2 {
		t.Fatalf("expected 2 envs, got %d", len(envs))
	}

	if !envs[0].enabled() {
		t.Errorf("expected env 0 to be enabled")
	}
	if envs[1].enabled() {
		t.Errorf("expected env 1 to be disabled")
	}

	crons, err := driver.ListCrons(ctx, "")
	if err != nil {
		t.Fatalf("ListCrons failed: %v", err)
	}
	if len(crons) != 2 {
		t.Fatalf("expected 2 crons, got %d", len(crons))
	}
	if !crons[0].enabled() {
		t.Errorf("expected cron 0 to be enabled")
	}
	if crons[1].enabled() {
		t.Errorf("expected cron 1 to be disabled")
	}
}

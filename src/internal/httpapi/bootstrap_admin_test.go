package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// ---- 零配置部署：自动管理员账号 ----
//
// 现象：不配 SMTP 时注册页用不了（注册要邮箱验证码），不配 YYB_ADMIN_PASSWORD
// 又没有既有账号，容器起来后没有任何入口。这里锁住三条：
//  1. AutoBootstrapAdmin 打开时，空库自动建 1 个管理员；
//  2. 二次启动不会重复创建；
//  3. 开关关闭（默认，测试与嵌入式用法）时绝不隐式建账号。

func countAppUsers(t *testing.T, app *App) int {
	t.Helper()
	count, err := app.auth.CountUsers(context.Background())
	if err != nil {
		t.Fatalf("CountUsers() error = %v", err)
	}
	return count
}

func TestAutoBootstrapAdminCreatesAccountWithoutConfiguredPassword(t *testing.T) {
	t.Setenv("GIN_MODE", "test")
	root := t.TempDir()
	cfg := Config{ResourceRoot: root, AuthDriver: "sqlite", AdminUser: "admin", AutoBootstrapAdmin: true}

	newApp := func(config Config) *App {
		t.Helper()
		created, err := NewApp(config)
		if err != nil {
			t.Fatalf("NewApp() error = %v", err)
		}
		t.Cleanup(func() { _ = created.Close() })
		return created
	}

	app := newApp(cfg)
	if got := countAppUsers(t, app); got != 1 {
		t.Fatalf("空库 + AutoBootstrapAdmin 应自动创建 1 个管理员，实际 %d", got)
	}

	// 二次启动：库里已有账号，BootstrapAdminIfEmpty 必须是 no-op。
	if got := countAppUsers(t, newApp(cfg)); got != 1 {
		t.Fatalf("重复启动不应再建账号，实际用户数 %d", got)
	}

	// 不指定 AdminUser 时也要落到默认账号名 admin（零配置部署的真实场景）。
	defaultApp := newApp(Config{ResourceRoot: t.TempDir(), AuthDriver: "sqlite", AutoBootstrapAdmin: true})
	if got := countAppUsers(t, defaultApp); got != 1 {
		t.Fatalf("未配置 YYB_ADMIN_USER 时应创建默认 admin，实际用户数 %d", got)
	}

	// 默认关闭：其它调用方（测试、嵌入式）不会有隐式账号。
	offApp := newApp(Config{ResourceRoot: t.TempDir(), AuthDriver: "sqlite", AdminUser: "admin"})
	if got := countAppUsers(t, offApp); got != 0 {
		t.Fatalf("AutoBootstrapAdmin 关闭时不应建账号，实际用户数 %d", got)
	}
}

// ---- 个人设置里的「接口令牌」----

type apiTokenPayload struct {
	Token       string `json:"token"`
	Masked      string `json:"masked"`
	Source      string `json:"source"`
	AllowNoAuth bool   `json:"allow_no_auth"`
	EnvName     string `json:"env_name"`
}

func decodeAPITokenPayload(t *testing.T, body string) apiTokenPayload {
	t.Helper()
	var envelope struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data apiTokenPayload `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode api token view: %v (body=%s)", err, body)
	}
	return envelope.Data
}

// TestAPITokenSettingsRotateKeepsGateClosed 覆盖「个人设置里重新生成令牌」：
// 新令牌立即生效、旧令牌立刻失效，且不需要重启容器。
func TestAPITokenSettingsRotateKeepsGateClosed(t *testing.T) {
	unsetAPITokenEnv(t)
	handler, app, cookie := smtpAdminSession(t, Config{ResourceRoot: t.TempDir(), AuthDriver: "sqlite"})

	rec := serveWithHeaders(t, handler, http.MethodGet, "/api/settings/api-token", "", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings/api-token status = %d, body = %s", rec.Code, rec.Body.String())
	}
	view := decodeAPITokenPayload(t, rec.Body.String())
	if view.Token == "" || view.Token != app.currentAPIToken() {
		t.Fatalf("视图里的令牌应与运行时令牌一致，view=%q runtime=%q", view.Token, app.currentAPIToken())
	}
	if view.Source != apiTokenSourceDatabase {
		t.Fatalf("自动生成的令牌 source 应为 database，实际 %q", view.Source)
	}
	if !strings.Contains(view.Masked, "*") || strings.Contains(view.Masked, view.Token) {
		t.Fatalf("掩码应隐藏中段，masked=%q token=%q", view.Masked, view.Token)
	}
	oldToken := view.Token

	rec = serveWithHeaders(t, handler, http.MethodPost, "/api/settings/api-token", "", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/settings/api-token status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rotated := decodeAPITokenPayload(t, rec.Body.String())
	if rotated.Token == "" || rotated.Token == oldToken {
		t.Fatalf("重新生成后令牌应变化，old=%q new=%q", oldToken, rotated.Token)
	}
	if app.currentAPIToken() != rotated.Token {
		t.Fatalf("运行时令牌未更新：runtime=%q new=%q", app.currentAPIToken(), rotated.Token)
	}

	// 旧令牌必须立刻失效，新令牌必须立刻可用。
	stale := serveWithHeaders(t, handler, http.MethodGet, "/instances", "", map[string]string{apiTokenHeader: oldToken}, nil)
	if stale.Code != http.StatusUnauthorized {
		t.Fatalf("旧令牌应被拒绝，实际状态码 %d", stale.Code)
	}
	fresh := serveWithHeaders(t, handler, http.MethodGet, "/instances", "", map[string]string{apiTokenHeader: rotated.Token}, nil)
	if fresh.Code != http.StatusOK {
		t.Fatalf("新令牌应放行 /instances，实际状态码 %d, body = %s", fresh.Code, fresh.Body.String())
	}
}

// TestAPITokenSettingsRefusesRotationWhenPinnedByEnv 锁住「环境变量优先」：
// 令牌由 YYB_API_TOKEN 固定时，界面只能读，重新生成要返回 409 并说明原因。
func TestAPITokenSettingsRefusesRotationWhenPinnedByEnv(t *testing.T) {
	t.Setenv(apiTokenEnvName, "pinned-by-env-token-0123456789")
	handler, _, cookie := smtpAdminSession(t, Config{ResourceRoot: t.TempDir(), AuthDriver: "sqlite"})

	rec := serveWithHeaders(t, handler, http.MethodGet, "/api/settings/api-token", "", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if view := decodeAPITokenPayload(t, rec.Body.String()); view.Source != apiTokenSourceEnv {
		t.Fatalf("source 应为 env，实际 %q", view.Source)
	}

	rec = serveWithHeaders(t, handler, http.MethodPost, "/api/settings/api-token", "", nil, cookie)
	if rec.Code != http.StatusConflict {
		t.Fatalf("环境变量固定时重新生成应返回 409，实际 %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), apiTokenEnvName) {
		t.Fatalf("拒绝原因应点名环境变量，实际 body = %s", rec.Body.String())
	}
}

// unsetAPITokenEnv 确保用例之间不串环境变量（t.Setenv 只在同一测试内生效，
// 这里额外保底，避免上一组用例遗留的 YYB_API_TOKEN 影响 source 判定）。
func unsetAPITokenEnv(t *testing.T) {
	t.Helper()
	t.Setenv(apiTokenEnvName, "")
}

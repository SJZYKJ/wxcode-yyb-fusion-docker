package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"yyb_go/internal/auth"
)

const sessionCookie = "yyb_session"

type loginAttempt struct {
	Failures int
	Start    time.Time
}

type authContextKey string

const authUserKey authContextKey = "user"
const authSessionKey authContextKey = "session"

func (a *App) requireBrowserSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		if a.auth == nil {
			c.Next()
			return
		}
		token, err := c.Cookie(sessionCookie)
		if err == nil {
			user, session, lookupErr := a.auth.UserBySession(c.Request.Context(), token)
			if lookupErr == nil {
				ctx := context.WithValue(c.Request.Context(), authUserKey, user)
				ctx = context.WithValue(ctx, authSessionKey, session)
				c.Request = c.Request.WithContext(ctx)
				c.Next()
				return
			}
		}
		clearSessionCookie(c.Writer, a.cookieSecure(c.Request))
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			writeError(c.Writer, http.StatusUnauthorized, "请先登录")
			c.Abort()
			return
		}
		next := c.Request.URL.RequestURI()
		http.Redirect(c.Writer, c.Request, "/login?next="+url.QueryEscape(next), http.StatusSeeOther)
		c.Abort()
	}
}

func (a *App) requireAdminSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		if a.auth == nil {
			c.Next()
			return
		}
		user := currentUser(c.Request)
		if user != nil && user.Role == "admin" {
			c.Next()
			return
		}
		if isManagementAPI(c.Request.URL.Path) {
			writeError(c.Writer, http.StatusForbidden, "需要管理员权限")
			c.Abort()
			return
		}
		// 普通用户误入管理员页面：回工作台，而不是个人设置页
		http.Redirect(c.Writer, c.Request, "/", http.StatusSeeOther)
		c.Abort()
	}
}

func isManagementAPI(path string) bool {
	for _, prefix := range []string{"/api/", "/accounts", "/qr", "/quick-login"} {
		if path == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodGet {
		if token, _ := r.Cookie(sessionCookie); token != nil {
			if _, _, err := a.auth.UserBySession(r.Context(), token.Value); err == nil {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		}
		serveFileOrText(w, r, filepath.Join(a.resources.Templates, "login.html"), fallbackLoginHTML)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Next     string `json:"next"`
	}
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	ip := a.clientIP(r)
	if !a.allowLogin(body.Username, ip) {
		writeError(w, http.StatusTooManyRequests, "登录失败次数过多，请 15 分钟后重试")
		return
	}
	user, err := a.auth.Authenticate(r.Context(), body.Username, body.Password)
	if err != nil {
		a.recordLoginFailure(body.Username, ip)
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	a.clearLoginFailures(body.Username, ip)
	token, _, err := a.auth.CreateSession(r.Context(), user.ID, r.UserAgent(), ip, a.cfg.SessionDuration)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建登录会话失败")
		return
	}
	setSessionCookie(w, token, a.cookieSecure(r), a.cfg.SessionDuration)
	next := safeNext(body.Next)
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "next": next})
}

func (a *App) handleRegister(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	allowed, reason := a.registrationAllowed(r)
	if !allowed {
		if r.Method == http.MethodGet {
			http.Redirect(w, r, "/login?registration=disabled", http.StatusSeeOther)
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, 405, "method not allowed")
			return
		}
		writeError(w, 403, reason)
		return
	}
	if r.Method == http.MethodGet {
		serveFileOrText(w, r, filepath.Join(a.resources.Templates, "register.html"), fallbackRegisterHTML)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}
	var body struct{ Username, DisplayName, Password string }
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	user, err := a.auth.RegisterUser(r.Context(), body.Username, body.DisplayName, body.Password)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	token, _, err := a.auth.CreateSession(r.Context(), user.ID, r.UserAgent(), a.clientIP(r), a.cfg.SessionDuration)
	if err != nil {
		writeError(w, 500, "创建登录会话失败")
		return
	}
	setSessionCookie(w, token, a.cookieSecure(r), a.cfg.SessionDuration)
	next := "/settings"
	if user.Role == "admin" {
		next = "/"
	}
	writeJSON(w, 201, map[string]any{"user": user, "next": next})
}

// registrationAllowed 决定这次注册请求是否放行，返回 (是否放行, 拒绝原因)。
//
// 默认策略是「关闭注册」，理由：任何能访问端口的人注册一个普通账号后就能进
// 工作台调用 /api/qinglong/* 等接口，而数据库里没有任何账号时第一个注册者
// 还会被直接提升为管理员——实例一旦暴露在公网，等于把控制权交出去。
//
// 放行条件（满足其一）：
//  1. YYB_ALLOW_REGISTRATION=true（运维显式开启，风险自负）；
//  2. 管理员在 /users 页面打开注册开关（数据库设置）；
//  3. 首次部署引导：数据库里还没有任何账号，且请求来自内网/回环地址。
//     这条是为了让「docker compose up -d 后直接扫个码开始用」仍然成立，
//     同时挡住公网上来抢注管理员的人。
func (a *App) registrationAllowed(r *http.Request) (bool, string) {
	ctx := r.Context()
	if a.cfg.AllowRegistration {
		return true, ""
	}
	enabled, err := a.auth.RegistrationEnabled(ctx)
	if err != nil {
		return false, "读取注册设置失败"
	}
	if enabled {
		return true, ""
	}
	count, countErr := a.auth.CountUsers(ctx)
	if countErr == nil && count == 0 && isLocalOrPrivate(a.clientIP(r)) {
		return true, ""
	}
	if countErr == nil && count == 0 {
		return false, "注册已关闭：实例还没有任何账号。请从内网访问完成首次注册，" +
			"或设置环境变量 YYB_ADMIN_USER / YYB_ADMIN_PASSWORD 指定管理员账号后重启。"
	}
	return false, "注册已关闭，请联系管理员创建账号"
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}
	if a.auth != nil {
		if cookie, err := r.Cookie(sessionCookie); err == nil {
			_ = a.auth.DeleteSession(r.Context(), cookie.Value)
		}
	}
	clearSessionCookie(w, a.cookieSecure(r))
	writeJSON(w, 200, map[string]any{"logged_out": true})
}

func (a *App) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}
	serveFileOrText(w, r, filepath.Join(a.resources.Templates, "settings.html"), fallbackSettingsHTML)
}
func (a *App) handleUsersPage(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}
	serveFileOrText(w, r, filepath.Join(a.resources.Templates, "users.html"), fallbackUsersHTML)
}

func (a *App) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}
	if a.auth == nil {
		writeJSON(w, 200, map[string]any{
			"auth_enabled": false,
			"session_id":   "",
			"user": map[string]any{
				"username":     "local",
				"display_name": "本机管理员",
				"role":         "admin",
				"enabled":      true,
			},
		})
		return
	}
	user, session := currentAuth(r)
	if user == nil || session == nil {
		writeError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	writeJSON(w, 200, map[string]any{"auth_enabled": true, "user": user, "session_id": session.ID})
}
func (a *App) handleProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, 405, "method not allowed")
		return
	}
	user, _ := currentAuth(r)
	var body struct {
		DisplayName string `json:"display_name"`
	}
	if decodeOptionalJSON(r, &body) != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	updated, err := a.auth.UpdateProfile(r.Context(), user.ID, body.DisplayName)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, updated)
}
func (a *App) handlePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, 405, "method not allowed")
		return
	}
	user, session := currentAuth(r)
	var body struct {
		Current string `json:"current_password"`
		Next    string `json:"new_password"`
	}
	if decodeOptionalJSON(r, &body) != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if err := a.auth.ChangePassword(r.Context(), user.ID, body.Current, body.Next, session.ID); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"updated": true})
}
func (a *App) handleSessions(w http.ResponseWriter, r *http.Request) {
	user, session := currentAuth(r)
	if r.Method == http.MethodGet {
		items, err := a.auth.ListSessions(r.Context(), user.ID)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"current_session_id": session.ID, "sessions": items})
		return
	}
	if r.Method == http.MethodDelete {
		if err := a.auth.DeleteOtherSessions(r.Context(), user.ID, session.ID); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"deleted": true})
		return
	}
	writeError(w, 405, "method not allowed")
}

func (a *App) handleUsers(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		users, err := a.auth.ListUsers(r.Context())
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, users)
		return
	}
	if r.Method == http.MethodPost {
		var body struct {
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
			Password    string `json:"password"`
			Role        string `json:"role"`
		}
		if decodeOptionalJSON(r, &body) != nil {
			writeError(w, 400, "请求格式错误")
			return
		}
		user, err := a.auth.CreateUser(r.Context(), body.Username, body.DisplayName, body.Password, body.Role)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		writeJSON(w, 201, user)
		return
	}
	writeError(w, 405, "method not allowed")
}
func (a *App) handleUserAction(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	actor, _ := currentAuth(r)
	parts := strings.Split(strings.Trim(r.URL.Path[len("/api/auth/users/"):], "/"), "/")
	if len(parts) != 2 {
		writeError(w, 404, "not found")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeError(w, 400, "无效用户 ID")
		return
	}
	switch parts[1] {
	case "state":
		var body struct {
			Role    string `json:"role"`
			Enabled bool   `json:"enabled"`
		}
		if r.Method != http.MethodPut || decodeOptionalJSON(r, &body) != nil {
			writeError(w, 400, "请求格式错误")
			return
		}
		updated, err := a.auth.UpdateUser(r.Context(), actor.ID, id, body.Role, body.Enabled)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, updated)
	case "password":
		var body struct {
			Password string `json:"password"`
		}
		if r.Method != http.MethodPut || decodeOptionalJSON(r, &body) != nil {
			writeError(w, 400, "请求格式错误")
			return
		}
		if err := a.auth.AdminResetPassword(r.Context(), id, body.Password); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"updated": true})
	case "delete":
		if r.Method != http.MethodDelete {
			writeError(w, 405, "method not allowed")
			return
		}
		if err := a.auth.DeleteUser(r.Context(), actor.ID, id); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"deleted": true})
	default:
		writeError(w, 404, "not found")
	}
}
func (a *App) handleRegistrationSetting(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		enabled, err := a.auth.RegistrationEnabled(r.Context())
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"enabled": enabled})
		return
	}
	if r.Method == http.MethodPut {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if decodeOptionalJSON(r, &body) != nil {
			writeError(w, 400, "请求格式错误")
			return
		}
		if err := a.auth.SetRegistrationEnabled(r.Context(), body.Enabled); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"enabled": body.Enabled})
		return
	}
	writeError(w, 405, "method not allowed")
}

func currentAuth(r *http.Request) (*auth.User, *auth.Session) {
	user, _ := r.Context().Value(authUserKey).(*auth.User)
	session, _ := r.Context().Value(authSessionKey).(*auth.Session)
	return user, session
}
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	user, _ := currentAuth(r)
	if user.Role != "admin" {
		writeError(w, 403, "需要管理员权限")
		return false
	}
	return true
}
func setSessionCookie(w http.ResponseWriter, token string, secure bool, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", MaxAge: int(ttl.Seconds()), HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}
func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}

// cookieSecure 决定这次响应里的会话 Cookie 是否带 Secure 标记。
// 除 YYB_COOKIE_SECURE=true 的显式配置外，请求本身是 HTTPS（直连 TLS 或
// 反代传来的 X-Forwarded-Proto: https）时也自动加上，避免 HTTPS 站点上
// 会话 Cookie 还能从明文 HTTP 侧被带出去。
//
// 注意 X-Forwarded-Proto 可伪造，但伪造者只能影响自己这次请求的响应——
// 顶多让自己拿到一个带 Secure 的 Cookie 从而登录不上，不构成对他人的攻击。
func (a *App) cookieSecure(r *http.Request) bool {
	if a.cfg.CookieSecure {
		return true
	}
	if r.TLS != nil {
		return true
	}
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		if proto := strings.TrimSpace(strings.Split(forwarded, ",")[0]); strings.EqualFold(proto, "https") {
			return true
		}
	}
	return false
}

// clientIP 返回用于登录限速与会话记录的客户端地址。
//
// 默认【不信任】X-Forwarded-For：该请求头由客户端完全控制，无条件采信会让
// 攻击者每次请求换一个伪造 IP，把按 IP 做的登录失败计数彻底绕开。
// 只有运维显式设置 YYB_TRUST_PROXY=true（确实部署在反代后面）时才采信它。
func (a *App) clientIP(r *http.Request) string {
	if a.cfg.TrustProxy {
		if value := firstForwardedFor(r); value != "" {
			return value
		}
		if value := strings.TrimSpace(r.Header.Get("X-Real-IP")); value != "" {
			return value
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

// 登录失败限速参数。
//
// 计数按「账号」和「来源 IP」两条线各自独立：只看 IP 会被伪造头/多 IP 绕开，
// 只看账号则单个账号被打爆时无法区分攻击者与本人误输。两条线任一超限即拒绝。
const (
	loginFailWindow  = 15 * time.Minute
	loginFailPerIP   = 8
	loginFailPerUser = 10
	// loginAttemptsMax 给计数表兜底，防止伪造来源刷爆内存。
	loginAttemptsMax = 4096
)

func loginIPKey(ip string) string    { return "ip:" + strings.TrimSpace(ip) }
func loginUserKey(name string) string { return "user:" + strings.ToLower(strings.TrimSpace(name)) }

func (a *App) allowLogin(username, ip string) bool {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	now := time.Now()
	for _, entry := range []struct {
		key   string
		limit int
	}{
		{loginIPKey(ip), loginFailPerIP},
		{loginUserKey(username), loginFailPerUser},
	} {
		if entry.key == "" {
			continue
		}
		attempt, ok := a.loginAttempts[entry.key]
		if !ok {
			continue
		}
		if now.Sub(attempt.Start) >= loginFailWindow {
			delete(a.loginAttempts, entry.key)
			continue
		}
		if attempt.Failures >= entry.limit {
			return false
		}
	}
	return true
}

func (a *App) recordLoginFailure(username, ip string) {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	for _, key := range []string{loginUserKey(username), loginIPKey(ip)} {
		if key == "" {
			continue
		}
		a.bumpFailureLocked(key, time.Now())
	}
}

func (a *App) bumpFailureLocked(key string, now time.Time) {
	attempt, ok := a.loginAttempts[key]
	if !ok {
		if len(a.loginAttempts) >= loginAttemptsMax {
			a.evictLoginAttemptsLocked(now)
		}
		attempt = loginAttempt{Start: now}
	} else if now.Sub(attempt.Start) >= loginFailWindow {
		attempt = loginAttempt{Start: now}
	}
	attempt.Failures++
	a.loginAttempts[key] = attempt
}

// evictLoginAttemptsLocked 在计数表满时腾位置：先清过期的，仍然满就淘汰
// 最早开始计数的一条。宁可漏掉一个攻击者的计数，也不要因为表满而拒绝
// 正常用户的登录（可用性优先，且表满本身不会带来越权）。
func (a *App) evictLoginAttemptsLocked(now time.Time) {
	for key, attempt := range a.loginAttempts {
		if now.Sub(attempt.Start) >= loginFailWindow {
			delete(a.loginAttempts, key)
		}
	}
	if len(a.loginAttempts) < loginAttemptsMax {
		return
	}
	var oldestKey string
	var oldest time.Time
	for key, attempt := range a.loginAttempts {
		if oldestKey == "" || attempt.Start.Before(oldest) {
			oldestKey, oldest = key, attempt.Start
		}
	}
	if oldestKey != "" {
		delete(a.loginAttempts, oldestKey)
	}
}

func (a *App) clearLoginFailures(username, ip string) {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	delete(a.loginAttempts, loginUserKey(username))
	delete(a.loginAttempts, loginIPKey(ip))
}

// safeNext 只接受站内的绝对路径，挡掉开放重定向。
//
// 除了 "//evil.com"（协议相对 URL），还必须拒绝 "/\evil.com"：按 WHATWG URL
// 规范浏览器会把路径里的反斜杠规范化为 "/"，于是 "/\evil.com" 会被当成
// "//evil.com" 跳到外站。所有控制字符一并剔除，避免响应头注入。
func safeNext(value string) string {
	value = strings.TrimSpace(value)
	var cleaned strings.Builder
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			continue
		}
		cleaned.WriteRune(r)
	}
	value = cleaned.String()
	if !strings.HasPrefix(value, "/") {
		return "/"
	}
	if strings.HasPrefix(value, "//") || strings.HasPrefix(value, "/\\") {
		return "/"
	}
	if strings.Contains(value, "\\") {
		return "/"
	}
	return value
}

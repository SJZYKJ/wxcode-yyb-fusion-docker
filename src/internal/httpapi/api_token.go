package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"yyb_go/internal/auth"
)

// apiTokenHeader 是脚本侧最省事的携带方式：X-API-Token: <token>。
const apiTokenHeader = "X-API-Token"

// requireAPIToken 保护"无需浏览器会话即可访问"的取码类接口
// （/login 取码分支、/instances、/whoami、/wxapp/*、/wx/*、/wxcode/*、/openapi.json）。
//
// fail-closed：令牌始终非空。App.cfg.APIToken 由 security.go 的
// resolveAPIToken 在启动时解析——显式配置优先，其次复用数据库里自动生成的
// 令牌，都没有就现场生成。只有运维显式设置 YYB_ALLOW_NO_AUTH=true
// 才会得到空字符串并放行全部请求（日志里有醒目警告）。
//
// 令牌非空时，满足以下任一条件即放行：
//  1. Authorization: Bearer <token>
//  2. X-API-Token: <token>
//  3. 查询参数 ?token=<token>（给不方便加请求头的旧脚本兜底）
//  4. 携带有效的浏览器登录会话 Cookie
//     （工作台的「调用配置」就是在浏览器里带 Cookie 调 /wxapp/*、/wx/code 的，
//     放行会话可保证 Web 界面不被自己的令牌拦住。）
//
// ⚠️ 放行的含义按凭据类型区分，不是“一律全权”：
//   - 令牌 → 公开 API 语义：不做归属过滤，可见性由账号的 api_shared 开关决定；
//   - 会话 → 控制台语义：按归属过滤，普通用户只能读写自己的账号（管理员全部）。
//
// 因此命中条件 4 时必须把会话用户注入请求上下文（browserSessionUser），
// 否则下游 handler 会把请求误判成“无会话的公开 API 调用”从而绕过归属过滤。
//
// bypass 是可选的免检判定，用于"同一路径既是对外接口又是网页入口"的场景
// （目前只有 /login：POST {username,password} 是控制台登录，必须放行）。
//
// 令牌比较用常量时间函数，避免按字符逐位比较带来的时序侧信道。
func (a *App) requireAPIToken(bypass ...func(*http.Request) bool) gin.HandlerFunc {
	expected := strings.TrimSpace(a.cfg.APIToken)
	return func(c *gin.Context) {
		for _, skip := range bypass {
			if skip != nil && skip(c.Request) {
				// 免检路径（控制台登录提交）：顺手注入会话身份，
				// 便于 handler 需要时识别当前用户。
				a.browserSessionUser(c)
				c.Next()
				return
			}
		}
		// 1. 显式令牌 -> 公开 API 语义（脚本身份），可见性由 api_shared 决定，
		//    不受浏览器会话影响。
		if expected != "" && apiTokenMatches(c.Request, expected) {
			c.Next()
			return
		}
		// 2. 浏览器会话 -> 注入身份后放行，下游按控制台权限模型（按归属）判定。
		//    早期实现只判“有没有会话”而不注入身份，于是任何登录用户都被当成
		//    公开 API 的全权调用方，能列出全部 openid 并取到他人（含管理员）的 code。
		if _, ok := a.browserSessionUser(c); ok {
			c.Next()
			return
		}
		// 3. YYB_ALLOW_NO_AUTH=true：无会话也放行，等价于本机管理员
		//    （此时令牌为空，见 security.go）。
		if expected == "" {
			c.Next()
			return
		}
		writeError(c.Writer, http.StatusUnauthorized,
			"缺少或无效的 API 令牌：请携带 Authorization: Bearer <YYB_API_TOKEN>、X-API-Token 请求头，或先登录 Web 控制台")
		c.Abort()
	}
}

// consoleLoginRequest 判定一次 /login 请求实际是 Web 控制台的账号密码登录。
//
// /login 是"一址两用"：GET /login?appId= 与 POST /login {"app_id":...} 取码，
// POST /login {"username":...} 才是控制台登录。若一律要求令牌，配了
// YYB_API_TOKEN 之后浏览器连登录页都提交不上去，所以必须放行后者。
//
// 判定条件与 handleUnifiedLogin 自身的分发逻辑保持一致（有 username 且无
// app_id 才走控制台登录），因此无法靠同时带 app_id 来绕过取码鉴权。
func consoleLoginRequest(r *http.Request) bool {
	if r.Method != http.MethodPost {
		// GET /login 是登录页；GET /login?appId= 走取码分支，不在这里放行。
		return strings.TrimSpace(r.URL.Query().Get("appId")) == "" &&
			strings.TrimSpace(r.URL.Query().Get("app_id")) == ""
	}
	if trimAll(r.URL.Query().Get("appId"), r.URL.Query().Get("app_id")) != "" {
		return false
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" && !strings.Contains(contentType, "application/json") {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return false
	}
	// handler 还要再读一次 body，必须原样放回。
	r.Body = io.NopCloser(bytes.NewReader(body))
	var probe map[string]json.RawMessage
	if json.Unmarshal(body, &probe) != nil {
		return false
	}
	_, hasAppID := probe["app_id"]
	_, hasUsername := probe["username"]
	return hasUsername && !hasAppID
}

func trimAll(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// apiTokenMatches 从请求头/查询参数中收集候选令牌并逐一常时比较。
func apiTokenMatches(r *http.Request, expected string) bool {
	candidates := make([]string, 0, 3)
	if raw := strings.TrimSpace(r.Header.Get("Authorization")); raw != "" {
		// 同时接受 "Bearer <token>" 与直接写令牌两种写法。
		if scheme, value, found := strings.Cut(raw, " "); found && strings.EqualFold(scheme, "Bearer") {
			candidates = append(candidates, strings.TrimSpace(value))
		} else {
			candidates = append(candidates, raw)
		}
	}
	if value := strings.TrimSpace(r.Header.Get(apiTokenHeader)); value != "" {
		candidates = append(candidates, value)
	}
	if value := strings.TrimSpace(r.URL.Query().Get("token")); value != "" {
		candidates = append(candidates, value)
	}
	for _, candidate := range candidates {
		if candidate != "" && subtle.ConstantTimeCompare([]byte(candidate), []byte(expected)) == 1 {
			return true
		}
	}
	return false
}

// browserSessionUser 识别请求携带的控制台登录会话，有效则把用户与会话注入
// 请求上下文并返回该用户。
//
// 它保证“被 requireAPIToken 放行”的会话请求，在下游 handler 里拥有与
// requireBrowserSession 完全一致的身份（currentUser(r) 非 nil），
// 从而正确落入控制台权限模型。未启用鉴权（a.auth == nil）时返回 false。
func (a *App) browserSessionUser(c *gin.Context) (*auth.User, bool) {
	if a.auth == nil {
		return nil, false
	}
	token, err := c.Cookie(sessionCookie)
	if err != nil || token == "" {
		return nil, false
	}
	user, session, lookupErr := a.auth.UserBySession(c.Request.Context(), token)
	if lookupErr != nil {
		return nil, false
	}
	ctx := context.WithValue(c.Request.Context(), authUserKey, user)
	ctx = context.WithValue(ctx, authSessionKey, session)
	c.Request = c.Request.WithContext(ctx)
	return user, true
}

// requireTokenOrAdminSession 保护设备侧引导/注册类接口
// （/wxcode/hookcfg、/wxcode/config、/wxcode/register）。
//
// 这些接口写的是全局 hook 实例表，其中的端口会被 deviceEndpoints() 拼成
// http://127.0.0.1:<port> 交给取码链路，属于服务器级配置。因此只允许：
//   - 携带 API 令牌的调用方（安卓 hook 客户端、自动化脚本）；
//   - 管理员会话（方便在浏览器控制台里调试验证）。
//
// 普通用户的会话一律拒绝，避免任意登录账号篡改他人的取码源。
func (a *App) requireTokenOrAdminSession() gin.HandlerFunc {
	expected := strings.TrimSpace(a.cfg.APIToken)
	return func(c *gin.Context) {
		if expected != "" && apiTokenMatches(c.Request, expected) {
			c.Next()
			return
		}
		if user, ok := a.browserSessionUser(c); ok {
			if user.Role == "admin" {
				c.Next()
				return
			}
			// 已登录但只是普通账号：明确属于权限不足。
			writeError(c.Writer, http.StatusForbidden, "该接口需要 API 令牌或管理员权限")
			c.Abort()
			return
		}
		// YYB_ALLOW_NO_AUTH=true：整体不设鉴权，等同本机管理员。
		if expected == "" {
			c.Next()
			return
		}
		// 既无令牌也无会话：属于未认证。
		writeError(c.Writer, http.StatusUnauthorized, "该接口需要 API 令牌")
		c.Abort()
	}
}

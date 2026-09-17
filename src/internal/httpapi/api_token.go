package httpapi

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// apiTokenHeader 是脚本侧最省事的携带方式：X-API-Token: <token>。
const apiTokenHeader = "X-API-Token"

// requireAPIToken 保护"无需浏览器会话即可访问"的取码类接口
// （/login 取码分支、/instances、/whoami、/wxapp/*、/wx/*、/openapi.json）。
//
// 设计目标：默认不改变任何既有部署的行为。
//
//   - YYB_API_TOKEN 未配置（空字符串）→ 完全放行，等价于旧版本；
//   - YYB_API_TOKEN 已配置 → 满足以下任一条件即放行：
//     1. Authorization: Bearer <token>
//     2. X-API-Token: <token>
//     3. 查询参数 ?token=<token>（给不方便加请求头的旧脚本兜底）
//     4. 携带有效的浏览器登录会话 Cookie
//     （工作台的「调用配置」就是在浏览器里带 Cookie 调 /wxapp/*、/wx/code 的，
//     放行会话可保证 Web 界面不被自己的令牌拦住。）
//
// bypass 是可选的免检判定，用于"同一路径既是对外接口又是网页入口"的场景
// （目前只有 /login：POST {username,password} 是控制台登录，必须放行）。
//
// 令牌比较用常量时间函数，避免按字符逐位比较带来的时序侧信道。
func (a *App) requireAPIToken(bypass ...func(*http.Request) bool) gin.HandlerFunc {
	expected := strings.TrimSpace(a.cfg.APIToken)
	return func(c *gin.Context) {
		if expected == "" {
			c.Next()
			return
		}
		for _, skip := range bypass {
			if skip != nil && skip(c.Request) {
				c.Next()
				return
			}
		}
		if apiTokenMatches(c.Request, expected) || a.hasBrowserSession(c.Request) {
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

// hasBrowserSession 判断请求是否带着一个仍然有效的控制台登录会话。
// 未启用鉴权（a.auth == nil）时不存在会话，返回 false。
func (a *App) hasBrowserSession(r *http.Request) bool {
	if a.auth == nil {
		return false
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	_, _, lookupErr := a.auth.UserBySession(r.Context(), cookie.Value)
	return lookupErr == nil
}

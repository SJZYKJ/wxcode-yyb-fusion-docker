package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"yyb_go/internal/store"
)

// ---------- unified /login dispatch (auto source selection) ----------

// handleUnifiedLogin dispatches the public /login route:
//
//   - GET  /login?appId=wx...      -> 兼容 wxcode 线格式的取码（原生扫码协议）
//   - POST /login {"app_id":...}   -> unified auto endpoint（原生扫码协议）
//   - everything else              -> console login page/session
func (a *App) handleUnifiedLogin(w http.ResponseWriter, r *http.Request) {
	appID := strings.TrimSpace(r.URL.Query().Get("appId"))
	if appID == "" {
		appID = strings.TrimSpace(r.URL.Query().Get("app_id"))
	}
	if appID != "" {
		a.handleWXLoginDevice(w, r)
		return
	}
	if r.Method == http.MethodPost {
		data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		r.Body = io.NopCloser(bytes.NewReader(data))
		if err == nil {
			var probe map[string]json.RawMessage
			if json.Unmarshal(data, &probe) == nil {
				_, hasAppID := probe["app_id"]
				_, hasUsername := probe["username"]
				if hasAppID && !hasUsername {
					a.handleUnifiedCode(w, r)
					return
				}
			}
		}
	}
	a.handleLogin(w, r)
}

// handleWXLoginDevice answers GET /login?appId= in the original wxcode wire
// format（err/msg/appId/status/code/codeType/codeLength），底层走原生扫码
// login_buffer 协议；可选 ref 指定账号，不带 ref 时取默认账号。
func (a *App) handleWXLoginDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	appID := strings.TrimSpace(r.URL.Query().Get("appId"))
	if appID == "" {
		appID = strings.TrimSpace(r.URL.Query().Get("app_id"))
	}
	if appID == "" {
		writeRawJSON(w, http.StatusOK, map[string]any{"err": -1, "msg": "appId is required"})
		return
	}
	// native login_buffer protocol mapped into the wxcode wire format.
	//    An optional ref query param selects the account explicitly; without
	//    one the default account is picked automatically (see defaultAccount).
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	native, openid, err := a.nativeCodeWithRef(r, ref, appID)
	if err != nil {
		writeRawJSON(w, http.StatusOK, map[string]any{"err": -1, "msg": err.Error()})
		return
	}
	code := stringFromAny(native["code"])
	writeRawJSON(w, http.StatusOK, map[string]any{
		"err": 0, "msg": "success", "appId": appID,
		"status": "ok", "code": code,
		"codeType": classifyCode(code), "codeLength": len(code),
		"openid": openid,
	})
}

// classifyCode mirrors the wxcode module's code-type classifier so fused
// responses carry the same codeType values as the original Xposed hook.
func classifyCode(code string) string {
	if code == "" {
		return "invalid"
	}
	switch {
	case regexp.MustCompile("^[0-9a-fA-F]+$").MatchString(code):
		return "hex"
	case regexp.MustCompile("^[A-Za-z0-9+/=]+$").MatchString(code):
		return "base64"
	case regexp.MustCompile("^[A-Za-z0-9_-]+$").MatchString(code):
		return "base64url"
	case regexp.MustCompile("^[A-Za-z0-9]+$").MatchString(code):
		return "alnum"
	default:
		return "other"
	}
}

// handleWXCompatInstances answers GET /instances with the wxcode wire shape.
// Native scan-login accounts are listed as running WeChat instances; when no
// account has been scanned the list is empty (count 0), same as wxcode
// before WeChat registers.
func (a *App) handleWXCompatInstances(w http.ResponseWriter, r *http.Request) {
	instances := make([]map[string]any, 0)
	// 可见范围按凭据类型分流（见 access.go 顶部注释）：
	//   - 带浏览器会话：控制台可见范围，普通用户只列归属自己的账号；
	//   - 带 API 令牌 / 未启用鉴权：脚本语义，列出全部 api_shared=1 的账号，
	//     保证既有脚本（顺丰/移动云盘等）仍能读到所有 code。
	// 早期版本无条件查 ListSharedAccounts，于是任何登录用户都能列出
	// 全部 openid（含管理员名下的），属横向越权，故改用 listReadableAccounts。
	accounts, err := a.listReadableAccounts(r)
	if err != nil {
		writeRawJSON(w, http.StatusInternalServerError, map[string]any{"err": -500, "msg": err.Error()})
		return
	}
	for _, acc := range accounts {
		instances = append(instances, map[string]any{
			"packageName": "com.tencent.mm",
			"userId":      0,
			"version":     "8.0.76",
			"port":        8088,
			"openid":      acc.OpenID,
			"nickname":    acc.Nickname,
		})
	}
	writeRawJSON(w, http.StatusOK, map[string]any{
		"count": len(instances), "instances": instances,
		"current": "com.tencent.mm", "currentUserId": 0,
	})
}

// handleUnifiedCode implements the fused auto endpoint（v11 起只剩原生扫码协议）。
// 带 ref 指定账号；不带 ref 取默认账号。安卓设备 Hook 取码已随 v11 移除。
func (a *App) handleUnifiedCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		AppID  string `json:"app_id"`
		Ref    string `json:"ref"`
		Prefer string `json:"prefer"`
	}
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	body.AppID = strings.TrimSpace(body.AppID)
	body.Ref = strings.TrimSpace(body.Ref)
	body.Prefer = strings.ToLower(strings.TrimSpace(body.Prefer))
	if body.AppID == "" {
		writeError(w, http.StatusBadRequest, "app_id is required")
		return
	}
	switch body.Prefer {
	case "", "auto", "native":
	default:
		writeError(w, http.StatusBadRequest, "prefer must be native (device sources were removed in v11)")
		return
	}
	nat, oid, err := a.nativeCodeWithRef(r, body.Ref, body.AppID)
	if err != nil {
		writeNativeCallError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source": "native", "fallback": false, "openid": oid, "result": nat,
	})
}

// nativeCodeWithRef resolves the native account (explicit ref, or the default
// account when no ref is given) and exchanges a code through the login_buffer
// protocol.
//
// 账号可见性按凭据类型判定（会话按归属、令牌按 api_shared），所以这里必须
// 接收 *http.Request 而不是裸 context —— 否则会话请求会被当成公开 API 调用，
// 从而取到他人（含管理员）的 code。
func (a *App) nativeCodeWithRef(r *http.Request, ref, appID string) (map[string]any, string, error) {
	ctx := r.Context()
	var acc *store.WechatAccount
	var err error
	if ref != "" {
		acc, err = a.resolveReadableAccount(r, ref)
	} else {
		acc, err = a.defaultAccountFor(r)
	}
	if err != nil {
		// 隔离拒绝（未共享/不属于当前会话/不存在）必须保持 403/404 语义，
		// 不能当作上游调用失败降级成 502。
		return nil, "", accountAccessDeniedError{msg: err.Error()}
	}
	result, err := a.invokeWXApp(ctx, acc, appID, nil, a.invokeGetCode)
	if err != nil {
		return nil, "", err
	}
	return result, acc.OpenID, nil
}

// accountAccessDeniedError marks account-resolution failures (not shared,
// not owned by the session, or not found) so callers can map them to
// 403/404 instead of a misleading 502.
type accountAccessDeniedError struct{ msg string }

func (e accountAccessDeniedError) Error() string { return e.msg }

// defaultAccount resolves the native account for a ref-less request. A single
// configured account is used directly; with several accounts the most recently
// active one is picked automatically (alive first, then latest updated) so
// wxcode-protocol callers that never send ref keep working. Callers that need
// a specific account should pass ref explicitly.
// defaultAccount 公开 API（令牌）语义下的默认账号：全部 api_shared=1 的账号里挑。
func (a *App) defaultAccount(ctx context.Context) (*store.WechatAccount, error) {
	accounts, err := a.db.ListSharedAccounts(ctx)
	if err != nil {
		return nil, err
	}
	return pickDefaultAccount(accounts)
}

// defaultAccountFor 按请求身份挑默认账号：
//   - 带浏览器会话：只在控制台可见范围内挑，普通用户不会因为“默认账号”
//     而取到别人的 code；
//   - 无会话（API 令牌 / 未启用鉴权）：与 defaultAccount 相同。
func (a *App) defaultAccountFor(r *http.Request) (*store.WechatAccount, error) {
	if currentUser(r) == nil {
		return a.defaultAccount(r.Context())
	}
	accounts, err := a.listReadableAccounts(r)
	if err != nil {
		return nil, err
	}
	return pickDefaultAccount(accounts)
}

// pickDefaultAccount 从候选账号里挑默认项：单个直接用，多个则选最近活跃的。
func pickDefaultAccount(accounts []*store.WechatAccount) (*store.WechatAccount, error) {
	switch len(accounts) {
	case 0:
		return nil, fmt.Errorf("no native WeChat accounts configured (scan a QR in the console first)")
	case 1:
		return accounts[0], nil
	}
	best := accounts[0]
	for _, acc := range accounts[1:] {
		if betterDefaultAccount(acc, best) {
			best = acc
		}
	}
	return best, nil
}

// betterDefaultAccount reports whether candidate is a better default than
// current: an alive account beats a non-alive one, otherwise the most recently
// updated account wins.
func betterDefaultAccount(candidate, current *store.WechatAccount) bool {
	cAlive := accountStatus(candidate) == "alive"
	curAlive := accountStatus(current) == "alive"
	if cAlive != curAlive {
		return cAlive
	}
	return candidate.UpdatedAt > current.UpdatedAt
}

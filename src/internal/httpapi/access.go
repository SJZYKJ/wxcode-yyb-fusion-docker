package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"yyb_go/internal/auth"
	"yyb_go/internal/store"
)

// ---------- 账号归属与可见性 ----------
//
// 鉴权未启用（a.auth == nil）等价于“本机管理员”：全部账号可见可管，与老行为一致。
//
// 权限模型由「凭据类型」决定，而不是「有没有浏览器会话」：
//
//  1. API 令牌（Authorization / X-API-Token / ?token=）——脚本通道：
//     不做归属过滤，能读到全部账号（含管理员名下的）；某个账号是否可见
//     由它的“允许脚本读取”（api_shared）开关决定。青龙脚本走这条路。
//
//  2. 浏览器会话 Cookie —— 控制台通道：
//     管理员：可见、可管全部账号；
//     普通用户：只能看见/操作归属自己的账号（wechat_accounts.owner_user_id = 自己）。
//
//     ⚠️ 会话绝不等价于令牌。早期实现把“有效会话”直接放行成公开 API，
//     于是任何登录用户（哪怕是最普通的账号）都能列出全部账号的 openid，
//     并借 api_shared 默认开启取到他人（含管理员）的 code，属于横向越权。
//     现按上面的模型判定，修正点见 canReadAccount / listReadableAccounts /
//     resolveReadableAccount / defaultAccountFor。
//
// 因此：公开 API 的 handler 判定可见性时必须走 canReadAccount 系列，
// 不要直接调 db.ListSharedAccounts / db.ResolveSharedAccount ——
// 那两条只认 api_shared，会跳过会话侧的归属判断。

func currentUser(r *http.Request) *auth.User {
	user, _ := r.Context().Value(authUserKey).(*auth.User)
	return user
}

// isAdminRequest 当前请求是否具备管理员视角。
// 未启用鉴权、或走公开 API（无会话）时都按管理员处理——公开 API 的可见性
// 由 api_shared 控制，而不是由归属控制。
func (a *App) isAdminRequest(r *http.Request) bool {
	if a.auth == nil {
		return true
	}
	user := currentUser(r)
	if user == nil {
		return true
	}
	return user.Role == "admin"
}

// requesterScope 返回控制台账号可见范围：归属用户 ID + 是否可见全部。
func (a *App) requesterScope(r *http.Request) (int64, bool) {
	if a.isAdminRequest(r) {
		return 0, true
	}
	return currentUser(r).ID, false
}

// canAccessAccount 控制台侧：该请求是否有权操作指定账号。
func (a *App) canAccessAccount(r *http.Request, acc *store.WechatAccount) bool {
	if a.isAdminRequest(r) {
		return true
	}
	return acc.OwnedBy(currentUser(r).ID)
}

// canReadAccountViaAPI 公开 API 侧：该请求是否有权读取指定账号。
//
//   - 带浏览器会话（控制台通道）：必须归属匹配。普通用户不能靠
//     api_shared 默认开启去读别人的账号，管理员照旧全可读。
//   - 无会话（令牌通道）：账号允许脚本读取（api_shared）即可。
func (a *App) canReadAccountViaAPI(r *http.Request, acc *store.WechatAccount) bool {
	if currentUser(r) != nil {
		return a.canAccessAccount(r, acc)
	}
	return acc.APIShared
}

// listVisibleAccounts 控制台可见账号列表。
func (a *App) listVisibleAccounts(r *http.Request) ([]*store.WechatAccount, error) {
	ownerID, all := a.requesterScope(r)
	return a.db.ListAccountsVisibleTo(r.Context(), ownerID, all)
}

// listReadableAccounts 返回本次请求可读的账号列表，按凭据类型分流：
//
//   - 带浏览器会话：控制台可见范围（普通用户仅自己的账号，管理员全部）；
//   - 无会话（API 令牌 / 未启用鉴权）：公开 API 语义，只列 api_shared=1。
//
// /instances 这类“既服务脚本又在浏览器里被访问”的接口必须用它，
// 不能用 db.ListSharedAccounts，否则会话用户会看到全部账号。
func (a *App) listReadableAccounts(r *http.Request) ([]*store.WechatAccount, error) {
	if currentUser(r) != nil {
		return a.listVisibleAccounts(r)
	}
	return a.db.ListSharedAccounts(r.Context())
}

// resolveAccountRefRaw 按 ref 查账号，不做任何权限判断。
func (a *App) resolveAccountRefRaw(w http.ResponseWriter, r *http.Request, ref string) (*store.WechatAccount, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		writeError(w, http.StatusBadRequest, "ref is required")
		return nil, false
	}
	acc, err := a.db.ResolveAccount(r.Context(), ref)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "account not found: "+ref)
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return nil, false
	}
	return acc, true
}

// resolveAccountRef 控制台侧解析账号：普通用户不能操作归属他人的账号。
func (a *App) resolveAccountRef(w http.ResponseWriter, r *http.Request, ref string) (*store.WechatAccount, bool) {
	acc, ok := a.resolveAccountRefRaw(w, r, ref)
	if !ok {
		return nil, false
	}
	if !a.canAccessAccount(r, acc) {
		writeError(w, http.StatusForbidden, "该账号归属其他用户，无权操作")
		return nil, false
	}
	return acc, true
}

// resolveAccountFromQuery 控制台侧按 ?ref= 解析账号。
func (a *App) resolveAccountFromQuery(w http.ResponseWriter, r *http.Request) (*store.WechatAccount, bool) {
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if ref == "" {
		writeError(w, http.StatusBadRequest, "ref query param is required")
		return nil, false
	}
	return a.resolveAccountRef(w, r, ref)
}

// resolveAPIAccountRef 公开 API 侧解析账号：会话按归属、令牌按 api_shared。
func (a *App) resolveAPIAccountRef(w http.ResponseWriter, r *http.Request, ref string) (*store.WechatAccount, bool) {
	acc, ok := a.resolveAccountRefRaw(w, r, ref)
	if !ok {
		return nil, false
	}
	if !a.canReadAccountViaAPI(r, acc) {
		if currentUser(r) != nil {
			writeError(w, http.StatusForbidden, "该账号归属其他用户，无权操作")
		} else {
			writeError(w, http.StatusForbidden, "该账号已关闭脚本读取（api_shared=false），请在控制台重新开启")
		}
		return nil, false
	}
	return acc, true
}

// resolveReadableAccount 是取码链路（/wxapp/getCode、/login 取码分支等）使用的
// ref 解析入口，与 resolveAPIAccountRef 同一套判定，只是返回 error 而不是直接写响应。
//
// 关键点：查库用 ResolveAccount（不带 api_shared 限制），再做归属/共享判定，
// 避免会话用户借“账号默认共享”取到别人的 code。
func (a *App) resolveReadableAccount(r *http.Request, ref string) (*store.WechatAccount, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("ref is required")
	}
	acc, err := a.db.ResolveAccount(r.Context(), ref)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("account not found: %s", ref)
		}
		return nil, err
	}
	if !a.canReadAccountViaAPI(r, acc) {
		// 不区分“不存在”与“无权”，避免向会话用户泄露账号归属信息。
		if currentUser(r) != nil {
			return nil, fmt.Errorf("account not found: %s", ref)
		}
		return nil, store.ErrAccountNotShared
	}
	return acc, nil
}

// ownerForScan 计算扫码/快捷登录时写入的归属：
//   - 未启用鉴权或无会话（本机管理员）：保持原归属（nil）
//   - 管理员：已有归属是别人的账号不夺走（nil 保持原样），其他情况记在自己名下
//   - 普通用户：扫码即认领（同一个人重新扫码刷新归属）
func (a *App) ownerForScan(ctx context.Context, r *http.Request, openid string) *int64 {
	if a.auth == nil {
		return nil
	}
	user := currentUser(r)
	if user == nil {
		return nil
	}
	if existing, err := a.db.GetAccountByOpenID(ctx, openid); err == nil && existing != nil {
		if existing.OwnerUserID != nil && *existing.OwnerUserID != user.ID && user.Role == "admin" {
			return nil
		}
	}
	id := user.ID
	return &id
}

// handleAccountShare 开关某个账号是否允许脚本/API 读取。
func (a *App) handleAccountShare(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/accounts/share" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		Ref    string `json:"ref"`
		Shared bool   `json:"shared"`
	}
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	acc, ok := a.resolveAccountRef(w, r, body.Ref)
	if !ok {
		return
	}
	if err := a.db.SetAccountShare(r.Context(), acc.ID, body.Shared); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, err := a.db.GetAccount(r.Context(), acc.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account": updated.Public(),
		"shared":  updated.APIShared,
	})
}

package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"yyb_go/internal/store"
	"yyb_go/internal/wxcode"
)

// ---------- unified /login dispatch (auto source selection) ----------

// handleUnifiedLogin dispatches the public /login route:
//
//   - GET  /login?appId=wx...      -> transparent wxcode device protocol
//   - POST /login {"app_id":...}   -> unified auto endpoint (device/native)
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
// format so existing wxcode clients work unchanged against the fused port.
// Device endpoints (on-device wxcode hook) are preferred when configured;
// when no device is available or it fails, the request transparently falls
// back to the native scan-login protocol and the result is mapped into the
// wxcode wire format (err/msg/appId/status/code/codeType/codeLength). This
// is the "fill in the wxcode API, the fusion adapts" behavior.
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
	// 1) on-device wxcode hook (rooted phone) when configured. Registered
	//    dual-app hooks (Android user id > 0) are discovered automatically.
	eps := a.deviceEndpoints()
	if len(eps) > 0 {
		client := wxcode.NewClient(eps, a.cfg.WXCodeTimeout)
		result, err := client.GetCode(r.Context(), appID)
		if err == nil {
			writeWXCodeLoginJSON(w, result)
			return
		}
		// device failed: fall through to the native scan-login protocol
	}
	// 2) native login_buffer protocol mapped into the wxcode wire format.
	//    An optional ref query param selects the account explicitly; without
	//    one the default account is picked automatically (see defaultAccount).
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	native, openid, err := a.nativeCodeWithRef(r.Context(), ref, appID)
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

// writeWXCodeLoginJSON renders a successful wxcode /login response.
func writeWXCodeLoginJSON(w http.ResponseWriter, result wxcode.LoginResult) {
	writeRawJSON(w, http.StatusOK, map[string]any{
		"err": result.Err, "msg": "success", "appId": result.AppID,
		"status": result.Status, "code": result.Code,
		"codeType": result.CodeType, "codeLength": result.CodeLength,
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

// handleWXCompatWhoami answers GET /whoami with the wxcode wire shape so
// existing wxcode clients can probe the fused gateway as if it were the
// on-device hook.
func (a *App) handleWXCompatWhoami(w http.ResponseWriter, r *http.Request) {
	entry, _ := a.hookMapping("8.0.76")
	port := a.cfg.WXCodeHookPort
	if port <= 0 {
		port = 8089
	}
	writeRawJSON(w, http.StatusOK, map[string]any{
		"packageName": "com.tencent.mm",
		"userId":      0,
		"port":        port,
		"version":     "8.0.76",
		"j1":          entry.J1,
		"c":           entry.C,
		"a1":          "com.tencent.mm." + entry.A1,
		"a7":          "com.tencent.mm." + entry.A7,
		"j1StaticMethod":   entry.J1Static,
		"j1InstanceMethod": entry.J1Instance,
	})
}

// handleWXCompatInstances answers GET /instances with the wxcode wire shape.
// Native scan-login accounts are listed as running WeChat instances; when no
// account has been scanned the list is empty (count 0), same as wxcode
// before WeChat registers.
func (a *App) handleWXCompatInstances(w http.ResponseWriter, r *http.Request) {
	instances := make([]map[string]any, 0)
	// On-device hooks that registered themselves (main WeChat plus dual-app
	// clones) are reported with their per-user ports, same as wxcode.
	for _, inst := range a.hookInstanceSnapshot() {
		instances = append(instances, map[string]any{
			"packageName": "com.tencent.mm",
			"userId":      inst.UserID,
			"version":     inst.Version,
			"port":        inst.Port,
		})
	}
	// 公开接口：列出“允许脚本读取”的全部账号（含管理员与其他用户名下的），
	// 不按归属过滤，保证既有脚本（顺丰/移动云盘等）能读到所有 code；
	// 拥有者在控制台关掉“允许脚本读取”的账号会从这里消失。
	accounts, err := a.db.ListSharedAccounts(r.Context())
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

// handleUnifiedCode implements the fused auto endpoint.
// It inspects the request parameters and selects the code source:
//
//   - only app_id            -> device (WeChat hook) first, native fallback
//   - app_id + ref           -> native (login_buffer) first, device fallback
//   - prefer=device|native   -> explicit override
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
	case "", "auto":
	case "device", "native":
	default:
		writeError(w, http.StatusBadRequest, "prefer must be device or native")
		return
	}

	deviceFirst := body.Prefer == "device" || (body.Prefer == "" && body.Ref == "")
	var (
		source     string
		fallback   bool
		openid     string
		result     map[string]any
		primaryErr error
	)
	if deviceFirst {
		dev, err := a.deviceCodeResult(r.Context(), body.AppID)
		if err == nil {
			source, fallback = "device", false
			result = dev
		} else {
			primaryErr = err
			if nat, oid, natErr := a.nativeCodeWithRef(r.Context(), body.Ref, body.AppID); natErr == nil {
				source, fallback, openid = "native", true, oid
				result = nat
			} else {
				primaryErr = fmt.Errorf("device failed: %v; native failed: %v", primaryErr, natErr)
			}
		}
	} else {
		nat, oid, err := a.nativeCodeWithRef(r.Context(), body.Ref, body.AppID)
		if err == nil {
			source, fallback, openid = "native", false, oid
			result = nat
		} else {
			primaryErr = err
			if dev, devErr := a.deviceCodeResult(r.Context(), body.AppID); devErr == nil {
				source, fallback = "device", true
				result = dev
			} else {
				primaryErr = fmt.Errorf("native failed: %v; device failed: %v", primaryErr, devErr)
			}
		}
	}
	if result == nil {
		writeError(w, http.StatusBadGateway, "code request failed: "+primaryErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source": source, "fallback": fallback, "openid": openid, "result": result,
	})
}

// nativeCodeWithRef resolves the native account (explicit ref, or the default
// account when no ref is given) and exchanges a code through the login_buffer
// protocol.
func (a *App) nativeCodeWithRef(ctx context.Context, ref, appID string) (map[string]any, string, error) {
	var acc *store.WechatAccount
	var err error
	if ref != "" {
		acc, err = a.resolveAccount(ctx, ref)
	} else {
		acc, err = a.defaultAccount(ctx)
	}
	if err != nil {
		return nil, "", err
	}
	result, err := a.invokeWXApp(ctx, acc, appID, nil, a.invokeGetCode)
	if err != nil {
		return nil, "", err
	}
	return result, acc.OpenID, nil
}

func (a *App) resolveAccount(ctx context.Context, ref string) (*store.WechatAccount, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("ref is required")
	}
	acc, err := a.db.ResolveSharedAccount(ctx, ref)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("account not found: %s", ref)
		}
		if errors.Is(err, store.ErrAccountNotShared) {
			return nil, fmt.Errorf("account not shared to api: %s", ref)
		}
		return nil, err
	}
	return acc, nil
}

// defaultAccount resolves the native account for a ref-less request. A single
// configured account is used directly; with several accounts the most recently
// active one is picked automatically (alive first, then latest updated) so
// wxcode-protocol callers that never send ref keep working. Callers that need
// a specific account should pass ref explicitly.
func (a *App) defaultAccount(ctx context.Context) (*store.WechatAccount, error) {
	accounts, err := a.db.ListSharedAccounts(ctx)
	if err != nil {
		return nil, err
	}
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

func (a *App) deviceCodeResult(ctx context.Context, appID string) (map[string]any, error) {
	eps := a.deviceEndpoints()
	if len(eps) == 0 {
		return nil, fmt.Errorf("device hook not configured (set WXCODE_URLS)")
	}
	client := wxcode.NewClient(eps, a.cfg.WXCodeTimeout)
	result, err := client.GetCode(ctx, appID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"appId": result.AppID, "status": result.Status, "code": result.Code,
		"codeType": result.CodeType, "codeLength": result.CodeLength,
	}, nil
}

// ---------- Zygisk hook bootstrap config ----------

type hookVersionEntry struct {
	J1         string `json:"j1"`
	C          string `json:"c"`
	A1         string `json:"a1"`
	A7         string `json:"a7"`
	J1Static   string `json:"j1_static_method"`
	J1Instance string `json:"j1_instance_method"`
}

var defaultHookMapping = map[string]hookVersionEntry{
	"8.0.49": {J1: "u70.k1", C: "o60.c", A1: "plugin.appbrand.jsapi.auth.b2", A7: "plugin.appbrand.jsapi.auth.f2", J1Static: "d", J1Instance: "f"},
	"8.0.62": {J1: "of0.j1", C: "he0.c", A1: "plugin.appbrand.jsapi.auth.h2", A7: "plugin.appbrand.jsapi.auth.l2", J1Static: "d", J1Instance: "g"},
	"8.0.70": {J1: "yj0.j1", C: "ti0.c", A1: "plugin.appbrand.jsapi.auth.h2", A7: "plugin.appbrand.jsapi.auth.l2", J1Static: "d", J1Instance: "g"},
	"8.0.71": {J1: "tk0.j1", C: "oj0.c", A1: "plugin.appbrand.jsapi.auth.h2", A7: "plugin.appbrand.jsapi.auth.l2", J1Static: "d", J1Instance: "g"},
	"8.0.72": {J1: "dl0.k1", C: "yj0.c", A1: "plugin.appbrand.jsapi.auth.h2", A7: "plugin.appbrand.jsapi.auth.l2", J1Static: "d", J1Instance: "g"},
	"8.0.74": {J1: "gm0.j1", C: "bl0.c", A1: "plugin.appbrand.jsapi.auth.h2", A7: "plugin.appbrand.jsapi.auth.l2", J1Static: "d", J1Instance: "g"},
	"8.0.76": {J1: "hm0.j1", C: "cl0.c", A1: "plugin.appbrand.jsapi.auth.i2", A7: "plugin.appbrand.jsapi.auth.m2", J1Static: "d", J1Instance: "g"},
}

// hookMapping resolves the WeChat internal class mapping for a version.
// Exact match wins, then the longest matching version prefix. Unknown
// versions fall back to the newest known mapping (same behavior as the
// original wxcode module defaults).
func (a *App) hookMapping(version string) (hookVersionEntry, bool) {
	mapping := make(map[string]hookVersionEntry, len(defaultHookMapping))
	for k, v := range defaultHookMapping {
		mapping[k] = v
	}
	merge := func(raw []byte) {
		var overlay struct {
			Common struct {
				J1Static   string `json:"j1_static_method"`
				J1Instance string `json:"j1_instance_method"`
			} `json:"common"`
			Versions map[string]hookVersionEntry `json:"versions"`
		}
		if json.Unmarshal(raw, &overlay) != nil {
			return
		}
		for ver, entry := range overlay.Versions {
			if entry.J1 == "" && entry.C == "" && entry.A1 == "" && entry.A7 == "" {
				continue
			}
			base := mapping[ver]
			if overlay.Common.J1Static != "" {
				base.J1Static = overlay.Common.J1Static
			}
			if overlay.Common.J1Instance != "" {
				base.J1Instance = overlay.Common.J1Instance
			}
			if entry.J1 != "" {
				base.J1 = entry.J1
			}
			if entry.C != "" {
				base.C = entry.C
			}
			if entry.A1 != "" {
				base.A1 = entry.A1
			}
			if entry.A7 != "" {
				base.A7 = entry.A7
			}
			if entry.J1Static != "" {
				base.J1Static = entry.J1Static
			}
			if entry.J1Instance != "" {
				base.J1Instance = entry.J1Instance
			}
			mapping[ver] = base
		}
	}
	if raw := os.Getenv("WXCODE_MAPPING"); strings.TrimSpace(raw) != "" {
		merge([]byte(raw))
	}
	if a.cfg.WXCodeMappingFile != "" {
		if data, err := os.ReadFile(a.cfg.WXCodeMappingFile); err == nil {
			merge(data)
		}
	}
	version = strings.TrimSpace(version)
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i := len(keys) - 1; i >= 0; i-- {
		key := keys[i]
		if version == key || (strings.HasPrefix(version, key) && len(version) > len(key) && version[len(key)] == '.') {
			return mapping[key], true
		}
	}
	if entry, ok := mapping["8.0.76"]; ok {
		return entry, false
	}
	if entry, ok := mapping["8.0.62"]; ok {
		return entry, false
	}
	return hookVersionEntry{J1: "of0.j1", C: "he0.c", A1: "plugin.appbrand.jsapi.auth.h2", A7: "plugin.appbrand.jsapi.auth.l2", J1Static: "d", J1Instance: "g"}, false
}

// handleHookCfg serves the bootstrap config consumed by the Zygisk hook
// running inside the WeChat process. Flat key/value text on purpose: the
// hook has no JSON parser.
func (a *App) handleHookCfg(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	port := a.cfg.WXCodeHookPort
	if port <= 0 {
		port = 8089
	}
	entry, matched := a.hookMapping(r.URL.Query().Get("v"))
	matchedInt := 0
	if matched {
		matchedInt = 1
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "port %d\n", port)
	fmt.Fprintf(w, "j1 %s\n", entry.J1)
	fmt.Fprintf(w, "c %s\n", entry.C)
	fmt.Fprintf(w, "a1 %s\n", entry.A1)
	fmt.Fprintf(w, "a7 %s\n", entry.A7)
	fmt.Fprintf(w, "j1_static %s\n", entry.J1Static)
	fmt.Fprintf(w, "j1_instance %s\n", entry.J1Instance)
	fmt.Fprintf(w, "matched %s\n", strconv.Itoa(matchedInt))
}
// ---------- on-device hook registration (dual-app support) ----------

// hookInstance describes an on-device wxcode hook (a WeChat process injected
// by the Zygisk module) that registered itself with the fused gateway.
type hookInstance struct {
	UserID  int
	Port    int
	Version string
	LastSeen time.Time
}

// hookInstanceTTL is how long a hook registration stays valid without a
// heartbeat. The Zygisk module re-registers on a timer, so stale entries
// (WeChat killed, dual-app removed) expire by themselves.
const hookInstanceTTL = 90 * time.Second

// deviceEndpoints merges the configured WXCODE_URLS with every on-device
// hook that registered itself. Dual-app WeChat instances (Android user id
// > 0) report their per-user ports via /wxcode/register, so they are usable
// without adding anything to WXCODE_URLS.
func (a *App) deviceEndpoints() []string {
	seen := map[string]bool{}
	var eps []string
	for _, e := range a.cfg.WXCodeURLs {
		e = strings.TrimSpace(e)
		if e != "" && !seen[e] {
			seen[e] = true
			eps = append(eps, e)
		}
	}
	for _, inst := range a.hookInstanceSnapshot() {
		e := fmt.Sprintf("http://127.0.0.1:%d", inst.Port)
		if !seen[e] {
			seen[e] = true
			eps = append(eps, e)
		}
	}
	return eps
}

// hookInstanceSnapshot returns the live registered hooks (within TTL).
func (a *App) hookInstanceSnapshot() []hookInstance {
	a.hookMu.Lock()
	defer a.hookMu.Unlock()
	now := time.Now()
	out := make([]hookInstance, 0, len(a.hookInstances))
	for userID, inst := range a.hookInstances {
		if now.Sub(inst.LastSeen) > hookInstanceTTL {
			delete(a.hookInstances, userID)
			continue
		}
		inst.UserID = userID
		out = append(out, inst)
	}
	return out
}

// handleWXCodeRegister accepts heartbeat registrations from the Zygisk hook
// running inside every WeChat process (including dual-app clones). Wire
// format mirrors the original wxcode /register endpoint:
//
//	GET /wxcode/register?port=<hookPort>&userId=<androidUserId>&version=<ver>
func (a *App) handleWXCodeRegister(w http.ResponseWriter, r *http.Request) {
	port, _ := strconv.Atoi(r.URL.Query().Get("port"))
	userID, _ := strconv.Atoi(r.URL.Query().Get("userId"))
	version := r.URL.Query().Get("version")
	if port <= 0 || port > 65535 {
		writeRawJSON(w, http.StatusBadRequest, map[string]any{"err": -1, "msg": "invalid port"})
		return
	}
	a.hookMu.Lock()
	if a.hookInstances == nil {
		a.hookInstances = map[int]hookInstance{}
	}
	a.hookInstances[userID] = hookInstance{Port: port, Version: version, LastSeen: time.Now()}
	a.hookMu.Unlock()
	writeRawJSON(w, http.StatusOK, map[string]any{"err": 0, "msg": "ok"})
}

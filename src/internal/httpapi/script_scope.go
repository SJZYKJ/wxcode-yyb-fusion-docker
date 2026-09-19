package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
)

// 脚本目录可见范围（全局一份）。
//
// 需求：管理员能设置「其他账号（非管理员）只能读取到青龙哪些文件夹内的脚本」。
// 管理员自己不受限；留空表示不限制（保持升级前的行为）。
//
// 约束是 fail-closed 的：非管理员不仅看不到白名单外的脚本，也不能把它们
// 挂到账号上（列表看不到 + 运行/开启定时都会被拒），否则只是「藏起来」，
// 拿接口直接调照样能跑。
const scriptDirAllowlistKey = "script_dir_allowlist"

// scriptScope 是一次请求能看到的脚本范围。
type scriptScope struct {
	all  bool
	dirs []string
}

// allows 判断脚本（相对脚本根目录的完整路径）是否在可见范围内。
func (s scriptScope) allows(path string) bool {
	if s.all {
		return true
	}
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return false
	}
	for _, dir := range s.dirs {
		if path == dir || strings.HasPrefix(path, dir+"/") {
			return true
		}
	}
	return false
}

// normalizeScriptDir 归一化一条白名单：去掉多余的斜杠与空白。
// 返回空串表示这条无效（忽略）。
func normalizeScriptDir(raw string) string {
	dir := strings.Trim(strings.TrimSpace(raw), "/")
	dir = strings.TrimSpace(dir)
	dir = strings.ReplaceAll(dir, `\`, "/")
	dir = strings.Trim(dir, "/")
	if dir == "" || dir == "." {
		return ""
	}
	return dir
}

// parseScriptDirAllowlist 解析设置值。兼容两种写法：
// JSON 数组（本程序写入的格式）与按行/逗号分隔的纯文本（方便手工改库）。
func parseScriptDirAllowlist(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	items := make([]string, 0)
	if strings.HasPrefix(raw, "[") {
		var list []string
		if err := json.Unmarshal([]byte(raw), &list); err == nil {
			items = list
		}
	}
	if items == nil {
		items = strings.FieldsFunc(raw, func(r rune) bool {
			return r == '\n' || r == '\r' || r == ',' || r == '，' || r == ';'
		})
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		dir := normalizeScriptDir(item)
		if dir == "" {
			continue
		}
		if _, exists := seen[dir]; exists {
			continue
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

func encodeScriptDirAllowlist(dirs []string) string {
	if len(dirs) == 0 {
		return ""
	}
	payload, err := json.Marshal(dirs)
	if err != nil {
		return ""
	}
	return string(payload)
}

// scriptDirAllowlist 读取全局白名单；未配置返回 nil（= 不限制）。
func (a *App) scriptDirAllowlist(ctx context.Context) ([]string, error) {
	raw, err := a.db.GetSetting(ctx, scriptDirAllowlistKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseScriptDirAllowlist(raw), nil
}

func (a *App) saveScriptDirAllowlist(ctx context.Context, dirs []string) error {
	return a.db.SetSetting(ctx, scriptDirAllowlistKey, encodeScriptDirAllowlist(dirs))
}

// scriptScopeForRequest 计算本次请求的脚本可见范围。
//
//   - 管理员 / 未启用鉴权 / 公开 API（无会话）：全部可见；
//   - 普通用户：只可见白名单目录；白名单为空表示不限制。
func (a *App) scriptScopeForRequest(r *http.Request) scriptScope {
	if a.isAdminRequest(r) {
		return scriptScope{all: true}
	}
	dirs, err := a.scriptDirAllowlist(r.Context())
	if err != nil {
		// 读不到设置时按「只看到什么都不行」处理，避免设置层故障变成越权。
		return scriptScope{dirs: []string{"\x00"}}
	}
	if len(dirs) == 0 {
		return scriptScope{all: true}
	}
	return scriptScope{dirs: dirs}
}

// filterVisibleScripts 按可见范围过滤脚本池。
func filterVisibleScripts(sources []scriptSource, scope scriptScope) []scriptSource {
	if scope.all {
		return sources
	}
	out := make([]scriptSource, 0, len(sources))
	for _, source := range sources {
		if scope.allows(source.Key) {
			out = append(out, source)
		}
	}
	return out
}

// handleScriptVisibility 管理员的「脚本目录可见范围」设置。
func (a *App) handleScriptVisibility(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		dirs, err := a.scriptDirAllowlist(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		payload := map[string]any{
			"dirs":           dirs,
			"restricted":     len(dirs) > 0,
			"available_dirs": []string{},
		}
		if dirs == nil {
			payload["dirs"] = []string{}
		}
		// 顺带把青龙脚本目录里现存的目录列出来，管理员不用手打。
		if a.qinglong.configured() {
			if scripts, err := a.qinglong.listScripts(r.Context()); err == nil {
				payload["available_dirs"] = collectScriptDirs(scripts)
			}
		}
		writeJSON(w, http.StatusOK, payload)
	case http.MethodPut:
		var body struct {
			Dirs []string `json:"dirs"`
		}
		if err := decodeOptionalJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		dirs := make([]string, 0, len(body.Dirs))
		seen := make(map[string]struct{}, len(body.Dirs))
		for _, item := range body.Dirs {
			dir := normalizeScriptDir(item)
			if dir == "" {
				continue
			}
			if len(dir) > 200 || strings.Contains(dir, "..") {
				writeError(w, http.StatusBadRequest, "目录名不合法: "+item)
				return
			}
			if _, exists := seen[dir]; exists {
				continue
			}
			seen[dir] = struct{}{}
			dirs = append(dirs, dir)
		}
		sort.Strings(dirs)
		if err := a.saveScriptDirAllowlist(r.Context(), dirs); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"dirs": dirs, "restricted": len(dirs) > 0})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// collectScriptDirs 归纳脚本池里出现过的目录，供管理员挑选用。
func collectScriptDirs(scripts []qingLongScript) []string {
	seen := make(map[string]struct{}, len(scripts))
	out := make([]string, 0, len(scripts))
	for _, script := range scripts {
		dir := normalizeScriptDir(script.Dir)
		if dir == "" {
			continue
		}
		if _, exists := seen[dir]; exists {
			continue
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

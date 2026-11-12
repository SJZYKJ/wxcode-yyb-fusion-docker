package httpapi

import (
	"log"
	"net/http"
	"os"
	"strings"
)

// apiTokenSourceEnv / apiTokenSourceDatabase / apiTokenSourceDisabled 说明令牌来源，
// 决定「重新生成」按钮是否可用：
//   - env      由 YYB_API_TOKEN 固定，界面只读，改令牌要改环境变量并重启；
//   - database 由容器自动生成并落库，界面可一键重新生成（立即生效、无需重启）；
//   - disabled 开了 YYB_ALLOW_NO_AUTH，取码接口不鉴权，讨论令牌没有意义。
const (
	apiTokenSourceEnv      = "env"
	apiTokenSourceDatabase = "database"
	apiTokenSourceDisabled = "disabled"
)

// apiTokenView 是「个人设置 → 接口令牌」卡片需要的数据。
type apiTokenView struct {
	Token       string `json:"token"`
	Masked      string `json:"masked"`
	Source      string `json:"source"`
	AllowNoAuth bool   `json:"allow_no_auth"`
	EnvName     string `json:"env_name"`
}

// apiTokenEnvName 是固定令牌时使用的环境变量名，前端直接展示给运维。
const apiTokenEnvName = "YYB_API_TOKEN"

func (a *App) apiTokenSource() string {
	if a.cfg.AllowNoAuth {
		return apiTokenSourceDisabled
	}
	// 显式配置的环境变量优先于数据库，与 resolveAPIToken 的解析顺序一致。
	if strings.TrimSpace(os.Getenv(apiTokenEnvName)) != "" {
		return apiTokenSourceEnv
	}
	return apiTokenSourceDatabase
}

// maskAPIToken 只留头 6 位与尾 4 位，避免整块令牌出现在截屏/录屏里。
func maskAPIToken(token string) string {
	if token == "" {
		return ""
	}
	if len(token) <= 12 {
		return strings.Repeat("*", len([]rune(token)))
	}
	return token[:6] + strings.Repeat("*", 12) + token[len(token)-4:]
}

func (a *App) apiTokenViewSnapshot() apiTokenView {
	token := a.currentAPIToken()
	return apiTokenView{
		Token:       token,
		Masked:      maskAPIToken(token),
		Source:      a.apiTokenSource(),
		AllowNoAuth: a.cfg.AllowNoAuth,
		EnvName:     apiTokenEnvName,
	}
}

// handleAPITokenSettings 提供「接口令牌」的查看（GET）与重新生成（POST）。
//
// 只挂在管理员路由组上：令牌是全局的，能取码就能取到所有开放账号的 code，
// 普通用户既不该看到也不该改。
func (a *App) handleAPITokenSettings(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.apiTokenViewSnapshot())
	case http.MethodPost:
		switch a.apiTokenSource() {
		case apiTokenSourceEnv:
			writeError(w, http.StatusConflict,
				"令牌由环境变量 YYB_API_TOKEN 固定，请修改环境变量后重启容器（界面无法覆盖）")
			return
		case apiTokenSourceDisabled:
			writeError(w, http.StatusConflict,
				"当前已开启 YYB_ALLOW_NO_AUTH（取码接口不鉴权），无需令牌；如需令牌请关闭该选项后重启")
			return
		}
		token := randomHexToken(32)
		if err := a.db.SetSetting(r.Context(), apiTokenSetting, token); err != nil {
			writeError(w, http.StatusInternalServerError, "写入新令牌失败："+err.Error())
			return
		}
		a.setAPIToken(token)
		log.Printf("[安全] 管理员在个人设置中重新生成了 API 访问令牌，旧令牌立即失效")
		log.Printf("[安全] 新令牌: %s", token)
		writeJSON(w, http.StatusOK, a.apiTokenViewSnapshot())
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

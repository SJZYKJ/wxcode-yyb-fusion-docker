package httpapi

// ---- SMTP 邮件设置（管理员在「用户管理」页配置） ----
//
// 背景：v16 起的注册流程要求邮箱验证码，而发信参数原先只能通过 YYB_SMTP_* 环境变量
// 提供——也就是说改一次邮件配置就要改 compose 文件 + 重启容器，管理员在界面上没法自助。
// 这里把邮件设置做成「可在控制台配置」的运行时设置，落库持久化（重启不丢），
// 并保留环境变量作为初始默认值：
//
//   优先级：数据库里的管理员设置 > 启动时的 YYB_SMTP_* 环境变量 > 未配置
//
// 安全取舍（重要，改动前先读完）：
//   · 读取接口【绝不回显密码】，只返回 PasswordSet 布尔值。前端提交时若密码留空，
//     表示「沿用已保存的密码」，而不是「把密码清空」——避免管理员改端口时误清密码。
//   · 该接口一律挂在 requireAdminSession 之后，且 handler 内再判一次管理员。
//   · 密码明文存库与 qinglong_client_secret 的既有做法一致（同一个 settings 表）；
//     数据库本身是本机卷，不额外引入加密密钥管理，避免「密钥也存在同一个库里」的假安全。

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// SMTP 设置项在 app_settings 表里的键名。逐项存储而不是整块 JSON，
// 是为了让「只改端口」这类操作不会因为读-改-写而覆盖掉并发改的其它字段。
const (
	smtpHostKey     = "smtp_host"
	smtpPortKey     = "smtp_port"
	smtpUsernameKey = "smtp_username"
	smtpPasswordKey = "smtp_password"
	smtpFromKey     = "smtp_from"
	smtpTLSKey      = "smtp_tls"
)

// smtpSettingsView 是对外返回的邮件设置视图：**不含密码明文**。
type smtpSettingsView struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	From        string `json:"from"`
	TLS         bool   `json:"tls"`
	PasswordSet bool   `json:"password_set"`
	// Configured 供前端直接判断「现在能不能发信」，避免前端重复实现校验规则。
	Configured bool `json:"configured"`
}

// loadSMTPSettings 从数据库读出邮件设置，缺项时回落到启动配置（环境变量）里的值。
func (a *App) loadSMTPSettings() SMTPConfig {
	base := a.cfg.SMTP
	read := func(key string) (string, bool) {
		value, err := a.db.GetSetting(a.settingsCtx(), key)
		if err != nil {
			return "", false
		}
		return value, true
	}
	if v, ok := read(smtpHostKey); ok {
		base.Host = v
	}
	if v, ok := read(smtpUsernameKey); ok {
		base.Username = v
	}
	if v, ok := read(smtpPasswordKey); ok {
		base.Password = v
	}
	if v, ok := read(smtpFromKey); ok {
		base.From = v
	}
	if v, ok := read(smtpPortKey); ok {
		if port, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && port > 0 {
			base.Port = port
		}
	}
	if v, ok := read(smtpTLSKey); ok {
		base.TLS = strings.TrimSpace(v) == "true"
	}
	return base
}

// activeSMTPConfig 返回当前【生效】的邮件配置。
// 发信与注册校验都必须走它，否则界面改了配置后仍按环境变量发信，出现「界面显示已配置、
// 实际发不出去」的鬼状态。
func (a *App) activeSMTPConfig() SMTPConfig { return a.loadSMTPSettings() }

// settingsCtx 给设置读写一个独立上下文：这些操作发生在启动期或管理请求里，
// 不应绑定到某个可能已被取消的请求上下文。
func (a *App) settingsCtx() context.Context { return context.Background() }

// handleSMTPSettings 处理 /api/auth/smtp：
//
//	GET  -> 返回当前设置（不含密码）
//	PUT  -> 更新设置（密码留空 = 沿用旧密码）
//	POST + {"action":"test"} -> 用当前设置真发一封测试邮件，验证配置是否正确
//
// 仅管理员可用（路由挂在 requireAdminSession 之后，这里再判一次）。
func (a *App) handleSMTPSettings(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg := a.activeSMTPConfig()
		writeJSON(w, http.StatusOK, smtpSettingsView{
			Host: cfg.Host, Port: cfg.Port, Username: cfg.Username, From: cfg.From,
			TLS: cfg.TLS, PasswordSet: strings.TrimSpace(cfg.Password) != "",
			Configured: cfg.Configured(),
		})
	case http.MethodPut:
		var body struct {
			Host     *string `json:"host"`
			Port     *int    `json:"port"`
			Username *string `json:"username"`
			Password *string `json:"password"`
			From     *string `json:"from"`
			TLS      *bool   `json:"tls"`
			// TestTo 非空时，保存后用新配置真发一封测试邮件。
			// 「保存成功」只能说明字段填满了，只有真发出去才证明配置可用。
			TestTo string `json:"test_to"`
		}
		if decodeOptionalJSON(r, &body) != nil {
			writeError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		updates := map[string]string{}
		if body.Host != nil {
			updates[smtpHostKey] = strings.TrimSpace(*body.Host)
		}
		if body.Username != nil {
			updates[smtpUsernameKey] = strings.TrimSpace(*body.Username)
		}
		if body.From != nil {
			updates[smtpFromKey] = strings.TrimSpace(*body.From)
		}
		if body.Port != nil {
			if *body.Port <= 0 || *body.Port > 65535 {
				writeError(w, http.StatusBadRequest, "端口应为 1-65535")
				return
			}
			updates[smtpPortKey] = strconv.Itoa(*body.Port)
		}
		if body.TLS != nil {
			updates[smtpTLSKey] = strconv.FormatBool(*body.TLS)
		}
		// 密码留空 = 沿用旧值。显式清空需要传 password_clear:true（当前不提供，
		// 避免管理员手滑把密码清掉导致注册功能静默失效）。
		if body.Password != nil && strings.TrimSpace(*body.Password) != "" {
			updates[smtpPasswordKey] = *body.Password
		}
		if len(updates) == 0 {
			writeError(w, http.StatusBadRequest, "没有需要更新的字段")
			return
		}
		// 先做一次「合并后的配置」合法性校验，避免存进去一个发不了信的半成品配置。
		merged := a.activeSMTPConfig()
		if v, ok := updates[smtpHostKey]; ok {
			merged.Host = v
		}
		if v, ok := updates[smtpUsernameKey]; ok {
			merged.Username = v
		}
		if v, ok := updates[smtpFromKey]; ok {
			merged.From = v
		}
		if v, ok := updates[smtpPasswordKey]; ok {
			merged.Password = v
		}
		if v, ok := updates[smtpPortKey]; ok {
			merged.Port, _ = strconv.Atoi(v)
		}
		if v, ok := updates[smtpTLSKey]; ok {
			merged.TLS = v == "true"
		}
		if err := validateSMTPConfig(merged); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		for key, value := range updates {
			if err := a.db.SetSetting(a.settingsCtx(), key, value); err != nil {
				writeError(w, http.StatusInternalServerError, "保存邮件设置失败")
				return
			}
		}
		// 配置变了就让发信器立即用新配置，否则仍在用旧的 cfg 发信。
		a.refreshEmailVerifier(a.activeSMTPConfig())
		saved := a.activeSMTPConfig()
		out := map[string]any{
			"host": saved.Host, "port": saved.Port, "username": saved.Username,
			"from": saved.From, "tls": saved.TLS,
			"password_set": strings.TrimSpace(saved.Password) != "",
			"configured":   saved.Configured(),
		}
		// 测试发信：失败不当作保存失败（设置已经存下了），而是把错误原样回给前端，
		// 让管理员看到「保存成功但发不出去」这个真实状态，而不是被一个假成功蒙住。
		if to := strings.TrimSpace(body.TestTo); to != "" {
			result := map[string]any{"to": to}
			if err := ValidateEmail(normalizeEmail(to)); err != nil {
				result["ok"] = false
				result["error"] = "收件地址格式不正确"
			} else if err := sendVerificationMail(saved, normalizeEmail(to), "123456"); err != nil {
				result["ok"] = false
				result["error"] = err.Error()
			} else {
				result["ok"] = true
			}
			out["test"] = result
		}
		writeJSON(w, http.StatusOK, out)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// validateSMTPConfig 只做「填得对不对」的形状校验，不联网。
// 真正的连通性由配置页的「发送测试邮件」来验证——那是唯一可信的判据。
func validateSMTPConfig(cfg SMTPConfig) error {
	if strings.TrimSpace(cfg.Host) == "" {
		return errors.New("请填写 SMTP 服务器地址")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return errors.New("端口应为 1-65535")
	}
	if strings.TrimSpace(cfg.Username) == "" {
		return errors.New("请填写 SMTP 用户名")
	}
	if strings.TrimSpace(cfg.Password) == "" {
		return errors.New("请填写 SMTP 密码")
	}
	if strings.TrimSpace(cfg.From) == "" {
		return errors.New("请填写发件人地址")
	}
	if !strings.Contains(cfg.From, "@") {
		return errors.New("发件人地址格式不正确")
	}
	return nil
}

// refreshEmailVerifier 用新配置重建发信器，让设置改动立即生效（无需重启容器）。
//
// 注意保留原有的验证码表与限流状态：直接换整个 verifier 会把这些内存状态清空，
// 正在等待验证码的用户会被强制重发。因此这里只替换 send 函数。
func (a *App) refreshEmailVerifier(cfg SMTPConfig) {
	if a.auth == nil {
		return
	}
	if a.emailCode == nil {
		if cfg.Configured() {
			a.emailCode = newEmailVerifier(newSMTPSender(cfg).SendVerificationCode)
		}
		return
	}
	if cfg.Configured() {
		a.emailCode.setSend(newSMTPSender(cfg).SendVerificationCode)
	} else {
		a.emailCode.setSend(nil)
	}
}

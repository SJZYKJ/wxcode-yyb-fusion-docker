package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"yyb_go/internal/store"
)

const (
	qingLongTypeSetting     = "qinglong_type"
	qingLongURLSetting      = "qinglong_url"
	qingLongClientIDSetting = "qinglong_client_id"
	qingLongSecretSetting   = "qinglong_client_secret"
)

type accountRemarkIn struct {
	Ref    string `json:"ref"`
	Remark string `json:"remark"`
}

type qingLongConfigIn struct {
	Type         string `json:"type"`
	URL          string `json:"url"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Clear        bool   `json:"clear"`
}

type qingLongSyncIn struct {
	Ref string `json:"ref"`
}

type qingLongAccountCleanup struct {
	Status            string
	EnvEntriesRemoved int
	TasksDeleted      int
}

type qingLongEnvChange struct {
	env      qingLongEnv
	newValue string
}

func (a *App) handleAccountRemark(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body accountRemarkIn
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	remark := strings.TrimSpace(body.Remark)
	if len([]rune(remark)) > 80 || strings.ContainsAny(remark, "\r\n\t") {
		writeError(w, http.StatusBadRequest, "账号备注不能超过 80 个字符或包含换行符")
		return
	}
	acc, ok := a.resolveAccountRef(w, r, body.Ref)
	if !ok {
		return
	}
	if err := a.db.SetAccountRemark(r.Context(), acc.ID, remark); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	acc, err := a.db.GetAccount(r.Context(), acc.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result := map[string]any{"account": acc.Public(), "jobs_updated": false}
	if a.qinglong.configured() {
		setting, settingErr := a.db.AccountPushSettingOrDefault(r.Context(), acc.ID)
		if settingErr == nil {
			settingErr = a.refreshAccountJobCommands(r.Context(), acc, setting)
		}
		if settingErr != nil {
			result["warning"] = "备注已保存，但面板任务名称更新失败：" + settingErr.Error()
		} else {
			result["jobs_updated"] = true
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) handleQingLongConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		pType, baseURL, clientID, secret := a.qinglong.configuration()
		writeJSON(w, http.StatusOK, map[string]any{
			"type":              pType,
			"url":               baseURL,
			"client_id":         clientID,
			"secret_configured": strings.TrimSpace(secret) != "",
			"configured":        a.qinglong.configured(),
		})
	case http.MethodPut:
		var body qingLongConfigIn
		if err := decodeOptionalJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if body.Clear {
			if err := a.persistQingLongConfig(r.Context(), PanelTypeQingLong, "", "", ""); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			a.qinglong.reconfigure(PanelTypeQingLong, "", "", "")
			writeJSON(w, http.StatusOK, map[string]any{"configured": false, "connected": false})
			return
		}
		pType := strings.ToLower(strings.TrimSpace(body.Type))
		if pType != PanelTypeDaidai {
			pType = PanelTypeQingLong
		}
		baseURL := strings.TrimRight(strings.TrimSpace(body.URL), "/")
		clientID := strings.TrimSpace(body.ClientID)
		_, _, _, currentSecret := a.qinglong.configuration()
		secret := strings.TrimSpace(body.ClientSecret)
		if secret == "" {
			secret = currentSecret
		}
		if err := validateQingLongConfig(baseURL, clientID, secret); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		candidate := newQingLongClient(pType, baseURL, clientID, secret, a.cfg.RequestTimeout)
		if err := candidate.status(r.Context()); err != nil {
			panelName := "面板"
			if pType == PanelTypeDaidai {
				panelName = "呆呆面板"
			} else {
				panelName = "青龙面板"
			}
			writeError(w, http.StatusBadGateway, panelName+"连接测试失败："+err.Error())
			return
		}
		if err := a.persistQingLongConfig(r.Context(), pType, baseURL, clientID, secret); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		a.qinglong.reconfigure(pType, baseURL, clientID, secret)
		writeJSON(w, http.StatusOK, map[string]any{
			"type": pType, "url": baseURL, "client_id": clientID, "secret_configured": true,
			"configured": true, "connected": true,
		})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) persistQingLongConfig(ctx context.Context, pType, baseURL, clientID, secret string) error {
	for key, value := range map[string]string{
		qingLongTypeSetting:     pType,
		qingLongURLSetting:      baseURL,
		qingLongClientIDSetting: clientID,
		qingLongSecretSetting:   secret,
	} {
		if err := a.db.SetSetting(ctx, key, value); err != nil {
			return err
		}
	}
	return nil
}

func validateQingLongConfig(baseURL, clientID, secret string) error {
	if baseURL == "" || clientID == "" || secret == "" {
		return errors.New("青龙地址、Client ID 和 Client Secret 均不能为空")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("青龙地址必须是有效的 http 或 https URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("青龙地址不能包含查询参数或片段")
	}
	return nil
}

func (a *App) handleQingLongSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !a.qinglong.configured() {
		writeError(w, http.StatusConflict, "请先配置青龙 OpenAPI")
		return
	}
	var body qingLongSyncIn
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	acc, ok := a.resolveAccountRef(w, r, body.Ref)
	if !ok {
		return
	}
	value, added, err := a.syncAccountToQingLong(r.Context(), acc)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account": acc.Public(), "name": "YYB_SERVER", "value": value, "added": added,
	})
}

func (a *App) syncAccountToQingLong(ctx context.Context, acc *store.WechatAccount) (string, bool, error) {
	envs, err := a.qinglong.listEnvs(ctx, "YYB_SERVER")
	if err != nil {
		return "", false, err
	}
	currentValue, remarks := "", "YYB Go 账号列表"
	for _, env := range envs {
		if env.Name == "YYB_SERVER" {
			currentValue = env.Value
			if strings.TrimSpace(env.Remarks) != "" {
				remarks = env.Remarks
			}
			break
		}
	}
	value, added := mergeYYBServerValue(currentValue, a.cfg.QingLongServer, acc)
	if err := a.qinglong.upsertEnv(ctx, "YYB_SERVER", value, remarks); err != nil {
		return "", false, err
	}
	return value, added, nil
}

func mergeYYBServerValue(existing, server string, acc *store.WechatAccount) (string, bool) {
	existing = strings.ReplaceAll(existing, "\r\n", "\n")
	existing = strings.TrimRight(existing, "\n")
	id := strconv.FormatInt(acc.ID, 10)
	for _, line := range strings.Split(existing, "\n") {
		separator := strings.LastIndex(strings.TrimSpace(line), "@")
		if separator < 0 {
			continue
		}
		ref := strings.TrimSpace(strings.TrimSpace(line)[separator+1:])
		if ref == id || (acc.OpenID != "" && ref == acc.OpenID) {
			return existing, false
		}
	}
	entry := strings.TrimSpace(server) + "@" + id
	if existing == "" {
		return entry, true
	}
	return existing + "\n" + entry, true
}

func removeAccountFromYYBServer(existing string, acc *store.WechatAccount) (string, int) {
	normalized := strings.ReplaceAll(existing, "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized == "" {
		return "", 0
	}

	id := strconv.FormatInt(acc.ID, 10)
	kept := make([]string, 0, strings.Count(normalized, "\n")+1)
	removed := 0
	for _, line := range strings.Split(normalized, "\n") {
		trimmed := strings.TrimSpace(line)
		separator := strings.LastIndex(trimmed, "@")
		if separator >= 0 {
			ref := strings.TrimSpace(trimmed[separator+1:])
			if ref == id || (acc.OpenID != "" && ref == acc.OpenID) {
				removed++
				continue
			}
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n"), removed
}

func (a *App) cleanupAccountFromQingLong(ctx context.Context, acc *store.WechatAccount) (qingLongAccountCleanup, error) {
	result := qingLongAccountCleanup{Status: "skipped"}
	if !a.qinglong.configured() {
		return result, nil
	}

	jobs, err := a.db.ListAccountScriptJobs(ctx, acc.ID)
	if err != nil {
		return result, err
	}
	envs, err := a.qinglong.listEnvs(ctx, "YYB_SERVER")
	if err != nil {
		return result, err
	}

	changes := make([]qingLongEnvChange, 0)
	for _, env := range envs {
		if env.Name != "YYB_SERVER" {
			continue
		}
		value, removed := removeAccountFromYYBServer(env.Value, acc)
		if removed == 0 {
			continue
		}
		changes = append(changes, qingLongEnvChange{env: env, newValue: value})
		result.EnvEntriesRemoved += removed
	}

	updated := make([]qingLongEnv, 0, len(changes))
	rollbackEnvs := func() {
		for i := len(updated) - 1; i >= 0; i-- {
			_ = a.qinglong.updateEnvEntry(ctx, updated[i], updated[i].Value)
		}
	}
	for _, change := range changes {
		if err := a.qinglong.updateEnvEntry(ctx, change.env, change.newValue); err != nil {
			rollbackEnvs()
			return result, err
		}
		updated = append(updated, change.env)
	}

	cronIDs := make([]int64, 0, len(jobs))
	seenCronIDs := make(map[int64]struct{}, len(jobs))
	for _, job := range jobs {
		if job.QLCronID <= 0 {
			continue
		}
		if _, exists := seenCronIDs[job.QLCronID]; exists {
			continue
		}
		seenCronIDs[job.QLCronID] = struct{}{}
		cronIDs = append(cronIDs, job.QLCronID)
	}
	if err := a.qinglong.deleteCrons(ctx, cronIDs); err != nil {
		rollbackEnvs()
		return result, err
	}

	result.Status = "completed"
	result.TasksDeleted = len(cronIDs)
	return result, nil
}

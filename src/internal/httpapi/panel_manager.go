package httpapi

import (
	"context"
	"strings"
	"sync"
	"time"
)

type panelManager struct {
	mu           sync.RWMutex
	panelType    string
	baseURL      string
	clientID     string
	clientSecret string
	timeout      time.Duration
	driver       PanelDriver
}

func newQingLongClient(panelType, baseURL, clientID, clientSecret string, timeout time.Duration) *qingLongClient {
	return newPanelManager(panelType, baseURL, clientID, clientSecret, timeout)
}

type qingLongClient = panelManager

func newPanelManager(panelType, baseURL, clientID, clientSecret string, timeout time.Duration) *panelManager {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	p := &panelManager{
		panelType:    normalizePanelType(panelType),
		baseURL:      strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		clientID:     strings.TrimSpace(clientID),
		clientSecret: strings.TrimSpace(clientSecret),
		timeout:      timeout,
	}
	p.driver = p.createDriver(p.panelType, p.baseURL, p.clientID, p.clientSecret)
	return p
}

func (p *panelManager) createDriver(pType, baseURL, clientID, secret string) PanelDriver {
	if normalizePanelType(pType) == PanelTypeDaidai {
		return newDaidaiDriver(baseURL, clientID, secret, p.timeout)
	}
	return newQingLongDriver(baseURL, clientID, secret, p.timeout)
}

func (p *panelManager) configured() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.baseURL != "" && p.clientID != "" && p.clientSecret != ""
}

func (p *panelManager) configuration() (string, string, string, string) {
	if p == nil {
		return PanelTypeQingLong, "", "", ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.panelType, p.baseURL, p.clientID, p.clientSecret
}

func (p *panelManager) getPanelType() string {
	if p == nil {
		return PanelTypeQingLong
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.panelType
}

func (p *panelManager) reconfigure(panelType, baseURL, clientID, clientSecret string) {
	panelType = normalizePanelType(panelType)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.panelType == panelType && p.baseURL == baseURL && p.clientID == clientID && p.clientSecret == clientSecret {
		return
	}
	p.panelType = panelType
	p.baseURL = baseURL
	p.clientID = clientID
	p.clientSecret = clientSecret
	p.driver = p.createDriver(panelType, baseURL, clientID, clientSecret)
}

func (p *panelManager) getDriver() PanelDriver {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.driver
}

func (p *panelManager) Status(ctx context.Context) error {
	return p.status(ctx)
}

func (p *panelManager) status(ctx context.Context) error {
	driver := p.getDriver()
	err := driver.Status(ctx)
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "405") {
		altType := PanelTypeQingLong
		if driver.PanelType() == PanelTypeQingLong {
			altType = PanelTypeDaidai
		}
		p.mu.RLock()
		altDriver := p.createDriver(altType, p.baseURL, p.clientID, p.clientSecret)
		p.mu.RUnlock()

		if altErr := altDriver.Status(ctx); altErr == nil {
			p.mu.Lock()
			p.panelType = altType
			p.driver = altDriver
			p.mu.Unlock()
			return nil
		}
	}
	return err
}

func (p *panelManager) ListEnvs(ctx context.Context, searchValue string) ([]qingLongEnv, error) {
	return p.listEnvs(ctx, searchValue)
}

func (p *panelManager) listEnvs(ctx context.Context, searchValue string) ([]qingLongEnv, error) {
	return p.getDriver().ListEnvs(ctx, searchValue)
}

func (p *panelManager) UpsertEnv(ctx context.Context, name, value, remarks string) error {
	return p.upsertEnv(ctx, name, value, remarks)
}

func (p *panelManager) upsertEnv(ctx context.Context, name, value, remarks string) error {
	return p.getDriver().UpsertEnv(ctx, name, value, remarks)
}

func (p *panelManager) UpdateEnv(ctx context.Context, id int64, name, value, remarks string) error {
	return p.updateEnv(ctx, id, name, value, remarks)
}

func (p *panelManager) updateEnv(ctx context.Context, id int64, name, value, remarks string) error {
	return p.getDriver().UpdateEnv(ctx, id, name, value, remarks)
}

func (p *panelManager) UpdateEnvEntry(ctx context.Context, env qingLongEnv, newValue string) error {
	return p.updateEnvEntry(ctx, env, newValue)
}

func (p *panelManager) updateEnvEntry(ctx context.Context, env qingLongEnv, newValue string) error {
	return p.getDriver().UpdateEnvEntry(ctx, env, newValue)
}

func (p *panelManager) SetEnvsEnabled(ctx context.Context, ids []int64, enabled bool) error {
	return p.setEnvsEnabled(ctx, ids, enabled)
}

func (p *panelManager) setEnvsEnabled(ctx context.Context, ids []int64, enabled bool) error {
	return p.getDriver().SetEnvsEnabled(ctx, ids, enabled)
}

func (p *panelManager) SetNamedEnvsEnabled(ctx context.Context, names []string, enabled bool) error {
	return p.setNamedEnvsEnabled(ctx, names, enabled)
}

func (p *panelManager) setNamedEnvsEnabled(ctx context.Context, names []string, enabled bool) error {
	return p.getDriver().SetNamedEnvsEnabled(ctx, names, enabled)
}

func (p *panelManager) ListCrons(ctx context.Context, search string) ([]qingLongCron, error) {
	return p.listCrons(ctx, search)
}

func (p *panelManager) listCrons(ctx context.Context, search string) ([]qingLongCron, error) {
	return p.getDriver().ListCrons(ctx, search)
}

func (p *panelManager) CreateCron(ctx context.Context, name, command, schedule, taskBefore, logName string) (*qingLongCron, error) {
	return p.createCron(ctx, name, command, schedule, taskBefore, logName)
}

func (p *panelManager) createCron(ctx context.Context, name, command, schedule, taskBefore, logName string) (*qingLongCron, error) {
	return p.getDriver().CreateCron(ctx, name, command, schedule, taskBefore, logName)
}

func (p *panelManager) UpdateCron(ctx context.Context, id int64, name, command, schedule, taskBefore, logName string) error {
	return p.updateCron(ctx, id, name, command, schedule, taskBefore, logName)
}

func (p *panelManager) updateCron(ctx context.Context, id int64, name, command, schedule, taskBefore, logName string) error {
	return p.getDriver().UpdateCron(ctx, id, name, command, schedule, taskBefore, logName)
}

func (p *panelManager) SetCronsEnabled(ctx context.Context, ids []int64, enabled bool) error {
	return p.setCronsEnabled(ctx, ids, enabled)
}

func (p *panelManager) setCronsEnabled(ctx context.Context, ids []int64, enabled bool) error {
	return p.getDriver().SetCronsEnabled(ctx, ids, enabled)
}

func (p *panelManager) RunCrons(ctx context.Context, ids []int64) error {
	return p.runCrons(ctx, ids)
}

func (p *panelManager) runCrons(ctx context.Context, ids []int64) error {
	return p.getDriver().RunCrons(ctx, ids)
}

func (p *panelManager) DeleteCrons(ctx context.Context, ids []int64) error {
	return p.deleteCrons(ctx, ids)
}

func (p *panelManager) deleteCrons(ctx context.Context, ids []int64) error {
	return p.getDriver().DeleteCrons(ctx, ids)
}

func (p *panelManager) CronLog(ctx context.Context, id int64) (string, error) {
	return p.cronLog(ctx, id)
}

func (p *panelManager) cronLog(ctx context.Context, id int64) (string, error) {
	return p.getDriver().CronLog(ctx, id)
}

func (p *panelManager) ListLogs(ctx context.Context) ([]qingLongLogEntry, error) {
	return p.listLogs(ctx)
}

func (p *panelManager) listLogs(ctx context.Context) ([]qingLongLogEntry, error) {
	return p.getDriver().ListLogs(ctx)
}

func (p *panelManager) LogDetail(ctx context.Context, dir, filename string) (string, error) {
	return p.logDetail(ctx, dir, filename)
}

func (p *panelManager) logDetail(ctx context.Context, dir, filename string) (string, error) {
	return p.getDriver().LogDetail(ctx, dir, filename)
}

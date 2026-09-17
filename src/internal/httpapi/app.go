package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	httpSwagger "github.com/swaggo/http-swagger/v2"

	"yyb_go/internal/auth"
	"yyb_go/internal/protocol"
	"yyb_go/internal/qr"
	"yyb_go/internal/store"
	"yyb_go/internal/wxcode"
)

type Config struct {
	ResourceRoot      string
	DBFilename        string
	TCPProxy          string
	SessionTTL        time.Duration
	RequestTimeout    time.Duration
	AvatarTimeout     time.Duration
	ScanTimeout       time.Duration
	QRSessionTTL      time.Duration
	KeepAliveInterval time.Duration
	KeepAliveAhead    time.Duration
	QingLongType      string
	QingLongURL       string
	QingLongClientID  string
	QingLongSecret    string
	QingLongServer    string
	QingLongRepo      string
	AuthDriver        string
	AuthDSN           string
	AuthMySQLDSN      string
	AdminUser         string
	AdminPassword     string
	CookieSecure      bool
	SessionDuration   time.Duration
	WXCodeURLs        []string
	WXCodeTimeout     time.Duration
	WXCodeHookPort    int
	WXCodeMappingFile string
	WXCodeRegisterTTL time.Duration
	// APIToken 保护所有"无需浏览器会话即可访问"的取码接口。
	// 启动时由 resolveAPIToken 解析（security.go），结果始终非空——
	// 未配置 YYB_API_TOKEN 会自动生成并落库，重启后不变。
	// 只有 AllowNoAuth 被显式打开时才可能为空。
	APIToken string
	// AllowNoAuth 对应 YYB_ALLOW_NO_AUTH=true：显式关闭取码接口鉴权。
	// 默认 false（fail-closed），开启后端口可达即可列出全部 openid 并取码。
	AllowNoAuth bool
	// AllowRegistration 对应 YYB_ALLOW_REGISTRATION=true：忽略数据库设置，
	// 始终允许公开注册。默认 false——注册关闭，只有内网地址能在
	// 「库中还没有任何账号」时完成首次管理员注册。
	AllowRegistration bool
	// TrustProxy 对应 YYB_TRUST_PROXY=true：信任 X-Forwarded-For / X-Real-IP。
	// 默认 false，登录限速等一律以 TCP 对端地址为准，避免伪造请求头绕过限速。
	TrustProxy bool
}

type App struct {
	cfg                Config
	resources          resources
	db                 *store.DB
	pool               *protocol.Pool
	qr                 *qr.Client
	refreshLoginBuffer func(context.Context, protocol.LoginBufferCredentials) (protocol.LoginBufferResult, error)
	exchangeAuthCode   func(context.Context, string) (protocol.LoginBufferResult, error)
	fetchUserInfo      func(context.Context, protocol.LoginBufferCredentials) (map[string]any, error)
	qinglong           *qingLongClient
	auth               *auth.Store

	wxcode        *wxcode.Client
	hookMu        sync.Mutex
	hookInstances map[int]hookInstance // userId -> registered hook instance
	mu            sync.Mutex
	qrSessions    map[string]*qr.Session
	quickSessions map[string]quickLoginSession
	refreshMu     sync.Mutex
	loginMu       sync.Mutex
	loginAttempts map[string]loginAttempt

	keepAliveCancel context.CancelFunc
	keepAliveDone   chan struct{}
}

var swaggerDocsHandler = httpSwagger.Handler(
	httpSwagger.URL("/openapi.json"),
	httpSwagger.DocExpansion("list"),
	httpSwagger.DeepLinking(true),
	httpSwagger.DefaultModelsExpandDepth(httpSwagger.ShowModel),
)

func NewApp(cfg Config) (*App, error) {
	if cfg.ResourceRoot == "" {
		cfg.ResourceRoot = filepath.Join(".", "resource")
	}
	if cfg.DBFilename == "" {
		cfg.DBFilename = DefaultDBFilename
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 8 * time.Second
	}
	if cfg.AvatarTimeout == 0 {
		cfg.AvatarTimeout = 10 * time.Second
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 30 * time.Minute
	}
	if cfg.QRSessionTTL == 0 {
		cfg.QRSessionTTL = 5 * time.Minute
	}
	if cfg.KeepAliveInterval > 0 && cfg.KeepAliveAhead <= 0 {
		cfg.KeepAliveAhead = 45 * time.Minute
	}
	if cfg.QingLongServer == "" {
		cfg.QingLongServer = "yyb-go:8000"
	}
	if cfg.QingLongRepo == "" {
		cfg.QingLongRepo = "SuperNaiBA_YYB-GO-Script,525815266_YYB-Go-Enhanced/scripts"
	}
	if cfg.SessionDuration <= 0 {
		cfg.SessionDuration = 7 * 24 * time.Hour
	}
	if cfg.WXCodeTimeout <= 0 {
		cfg.WXCodeTimeout = 40 * time.Second
	}
	if cfg.WXCodeRegisterTTL <= 0 {
		cfg.WXCodeRegisterTTL = 90 * time.Second
	}
	res, err := ensureResources(cfg.ResourceRoot)
	if err != nil {
		return nil, err
	}
	dbPath, err := prepareDBPath(res.DB, cfg.DBFilename)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	loadSetting := func(key, fallback string) string {
		value, settingErr := db.GetSetting(context.Background(), key)
		if settingErr == nil {
			return value
		}
		return fallback
	}
	cfg.QingLongType = loadSetting(qingLongTypeSetting, cfg.QingLongType)
	cfg.QingLongURL = loadSetting(qingLongURLSetting, cfg.QingLongURL)
	cfg.QingLongClientID = loadSetting(qingLongClientIDSetting, cfg.QingLongClientID)
	cfg.QingLongSecret = loadSetting(qingLongSecretSetting, cfg.QingLongSecret)
	// fail-closed：没有显式令牌时自动生成并落库，绝不让取码接口裸奔。
	apiToken, generated := resolveAPIToken(context.Background(), db, cfg.APIToken, cfg.AllowNoAuth)
	cfg.APIToken = apiToken
	announceAPIToken(apiToken, generated, cfg.AllowNoAuth)
	poolCfg := protocol.DefaultConfig()
	poolCfg.SessionTTL = cfg.SessionTTL
	poolCfg.ShortlinkTimeout = cfg.RequestTimeout
	poolCfg.TCPProxy = cfg.TCPProxy
	pool := protocol.NewPool(poolCfg, db)
	qrClient := qr.NewClient(cfg.RequestTimeout)
	app := &App{
		cfg:                cfg,
		resources:          res,
		db:                 db,
		pool:               pool,
		qr:                 qrClient,
		refreshLoginBuffer: qrClient.RefreshLoginBuffer,
		exchangeAuthCode:   qrClient.GetLoginBufferFromCode,
		fetchUserInfo:      qrClient.LoginBuffers().FetchUserInfo,
		qinglong:           newQingLongClient(cfg.QingLongType, cfg.QingLongURL, cfg.QingLongClientID, cfg.QingLongSecret, cfg.RequestTimeout),
		wxcode:             wxcode.NewClient(cfg.WXCodeURLs, cfg.WXCodeTimeout),
		hookInstances:      map[int]hookInstance{},
		qrSessions:         map[string]*qr.Session{},
		quickSessions:      map[string]quickLoginSession{},
		loginAttempts:      map[string]loginAttempt{},
	}
	authDriver := strings.ToLower(strings.TrimSpace(cfg.AuthDriver))
	authDSN := strings.TrimSpace(cfg.AuthDSN)
	if authDriver == "" && cfg.AuthMySQLDSN != "" {
		authDriver = "mysql"
		authDSN = cfg.AuthMySQLDSN
	}
	if authDriver != "" && authDriver != "none" {
		if authDriver == "mysql" && authDSN == "" {
			authDSN = cfg.AuthMySQLDSN
		}
		if authDriver == "sqlite" && authDSN == "" {
			authDSN = filepath.Join(res.DB, "auth.db")
		}
		if authDSN == "" {
			_ = db.Close()
			return nil, fmt.Errorf("auth %s DSN is empty", authDriver)
		}
		authCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		authStore, authErr := auth.Open(authCtx, authDriver, authDSN)
		if authErr != nil {
			_ = db.Close()
			return nil, authErr
		}
		if authErr = authStore.BootstrapAdmin(authCtx, cfg.AdminUser, cfg.AdminPassword); authErr != nil {
			_ = authStore.Close()
			_ = db.Close()
			return nil, fmt.Errorf("bootstrap admin: %w", authErr)
		}
		app.auth = authStore
	}
	app.startKeepAlive()
	return app, nil
}

func (a *App) Close() error {
	if a.keepAliveCancel != nil {
		a.keepAliveCancel()
		<-a.keepAliveDone
		a.keepAliveCancel = nil
	}
	if a.db != nil {
		if a.auth != nil {
			_ = a.auth.Close()
		}
		return a.db.Close()
	}
	return nil
}

func (a *App) Handler() http.Handler {
	if os.Getenv(gin.EnvGinMode) == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())

	// —— 永远开放：容器健康检查与登录页静态资源 ——
	router.Any("/health", func(c *gin.Context) {
		writeJSON(c.Writer, http.StatusOK, gin.H{"ok": true})
	})
	router.StaticFS("/static", http.Dir(a.resources.Static))

	// —— 无需浏览器会话：Web 登录 / 注册 / 登出 ——
	router.Any("/register", gin.WrapF(a.handleRegister))
	router.Any("/logout", gin.WrapF(a.handleLogout))

	// —— 公开取码接口：受 YYB_API_TOKEN 保护 ——
	// 令牌由启动时解析（显式配置 / 数据库自动生成），永远是非空的 fail-closed；
	// 只有显式设置 YYB_ALLOW_NO_AUTH=true 才会真的开放。
	// 现有自动化客户端（青龙脚本）不需要浏览器会话，但要带上令牌；
	// 工作台「调用配置」在浏览器里带会话调用，因此同样放行。
	//
	// /login 一址两用，单独注册：GET /login?appId= 与 POST {"app_id":..} 取码，
	// 需要令牌；而 POST {"username":..} 是控制台登录、GET /login 是登录页，
	// 必须放行，否则配了令牌后浏览器无法登录。
	router.Any("/login", a.requireAPIToken(consoleLoginRequest), gin.WrapF(a.handleUnifiedLogin))

	api := router.Group("/", a.requireAPIToken())
	// wxcode-compatible endpoints so existing wxcode clients work
	// unchanged against the fused gateway (wire format identical to the
	// original on-device NanoHTTPD service).
	api.Any("/whoami", gin.WrapF(a.handleWXCompatWhoami))
	api.Any("/instances", gin.WrapF(a.handleWXCompatInstances))
	// 设备 hook 的引导配置与心跳注册用更严格的中间件：注册接口会写全局
	// hookInstances，而 deviceEndpoints() 会把注册进来的端口拼成
	// http://127.0.0.1:<port> 供取码链路请求——放开就等于对外开放了
	// 一个 SSRF/取码源劫持点。故只接受 API 令牌或管理员会话，
	// 普通用户的浏览器会话同样会被拒。
	device := router.Group("/", a.requireTokenOrAdminSession())
	device.Any("/wxcode/hookcfg", gin.WrapF(a.handleHookCfg))
	device.Any("/wxcode/config", gin.WrapF(a.handleHookCfg))
	device.Any("/wxcode/register", gin.WrapF(a.handleWXCodeRegister))
	api.Any("/wx/oauth", gin.WrapF(a.handlePublicOAuth))
	api.Any("/wxapp/getCode", gin.WrapF(a.handleGetCode))
	api.Any("/wxapp/getPhoneNumber", gin.WrapF(a.handleGetPhoneNumber))
	api.Any("/wxapp/operateWxData", gin.WrapF(a.handleOperateWXData))
	api.Any("/wx/code", gin.WrapF(a.handleWXCodeAlias))
	api.Any("/wx/getuserinfo", gin.WrapF(a.handleWXGetUserInfo))
	api.Any("/wx/encryptkey", gin.WrapF(a.handleWXEncryptKey))
	api.Any("/wx/getphonenumber", gin.WrapF(a.handleWXPhoneAlias))
	api.Any("/wx/cloud", gin.WrapF(a.handleWXCloud))
	api.Any("/wx/qrcodeauth", gin.WrapF(a.handleQRRoot))
	api.Any("/wx/qrcodeauth/*path", gin.WrapF(a.handleQR))
	api.Any("/wx/mpgeta8key", gin.WrapF(a.handleWXMPGetA8Key))
	api.Any("/wx/appmsgext", gin.WrapF(a.handleWXAppMsgExt))
	api.Any("/wx/appmsglike", gin.WrapF(a.handleWXAppMsgLike))
	api.Any("/wxapp/deviceCode", gin.WrapF(a.handleDeviceGetCode))
	api.Any("/wx/devicecode", gin.WrapF(a.handleDeviceGetCode))
	api.Any("/openapi.json", gin.WrapF(a.handleOpenAPI))

	// ---- 以下需要浏览器会话 ----
	router.Use(a.requireBrowserSession())

	// —— 所有已登录用户可用 ——
	// 普通用户同样能进工作台、扫码添加账号、跑运行管理，但只能看到/操作
	// 归属自己（wechat_accounts.owner_user_id = 自己）的账号；管理员看全部。
	router.Any("/settings", gin.WrapF(a.handleSettingsPage))
	router.Any("/api/auth/me", gin.WrapF(a.handleAuthMe))
	router.Any("/api/auth/profile", gin.WrapF(a.handleProfile))
	router.Any("/api/auth/password", gin.WrapF(a.handlePassword))
	router.Any("/api/auth/sessions", gin.WrapF(a.handleSessions))
	router.Any("/api/wxcode/status", gin.WrapF(a.handleWXCodeStatus))
	router.Any("/", gin.WrapF(a.handleIndex))
	router.Any("/scan", gin.WrapF(a.handleScan))
	router.Any("/runs", gin.WrapF(a.handleRuns))
	router.Any("/docs", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/docs/index.html")
	})
	router.Any("/docs/*path", gin.WrapF(a.handleDocs))
	router.Any("/qr", gin.WrapF(a.handleQRRoot))
	router.Any("/qr/*path", gin.WrapF(a.handleQR))
	router.Any("/quick-login", gin.WrapF(a.handleQuickLoginRoot))
	router.Any("/quick-login/*path", gin.WrapF(a.handleQuickLogin))
	router.Any("/accounts", gin.WrapF(a.handleAccountsRoot))
	router.Any("/accounts/avatar", gin.WrapF(a.handleAccountAvatar))
	router.Any("/accounts/refresh", gin.WrapF(a.handleAccountRefresh))
	router.Any("/accounts/resync", gin.WrapF(a.handleAccountResync))
	router.Any("/accounts/remark", gin.WrapF(a.handleAccountRemark))
	router.Any("/accounts/share", gin.WrapF(a.handleAccountShare))
	router.Any("/api/qinglong/status", gin.WrapF(a.handleQingLongStatus))
	router.Any("/api/qinglong/sync", gin.WrapF(a.handleQingLongSync))
	router.Any("/api/qinglong/jobs", gin.WrapF(a.handleQingLongJobs))
	router.Any("/api/qinglong/jobs/enable", gin.WrapF(a.handleQingLongJobEnable))
	router.Any("/api/qinglong/jobs/run", gin.WrapF(a.handleQingLongJobRun))
	router.Any("/api/qinglong/jobs/log", gin.WrapF(a.handleQingLongJobLog))
	router.Any("/api/qinglong/runs", gin.WrapF(a.handleQingLongRuns))
	router.Any("/api/qinglong/runs/log", gin.WrapF(a.handleQingLongRunLog))
	router.Any("/api/qinglong/push", gin.WrapF(a.handleQingLongPush))

	// —— 仅管理员：用户管理、注册开关、面板服务器级配置 ——
	router.Use(a.requireAdminSession())
	router.Any("/users", gin.WrapF(a.handleUsersPage))
	router.Any("/api/auth/users", gin.WrapF(a.handleUsers))
	router.Any("/api/auth/users/*path", gin.WrapF(a.handleUserAction))
	router.Any("/api/auth/registration", gin.WrapF(a.handleRegistrationSetting))
	router.Any("/api/qinglong/config", gin.WrapF(a.handleQingLongConfig))
	// Keep the shorter /wx/* names used by existing YYB clients. The handlers
	// share the same session and retry logic as the canonical /wxapp/* routes.
	router.NoRoute(func(c *gin.Context) {
		writeError(c.Writer, http.StatusNotFound, "not found")
	})

	return router
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	serveFileOrText(w, r, filepath.Join(a.resources.Templates, "index.html"), fallbackIndexHTML)
}

func (a *App) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	serveFileOrText(w, r, filepath.Join(a.resources.Templates, "scan.html"), fallbackScanHTML)
}

func (a *App) handleDocs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if r.URL.Path == "/docs/" {
		http.Redirect(w, r, "/docs/index.html", http.StatusMovedPermanently)
		return
	}
	swaggerDocsHandler.ServeHTTP(w, r)
}

func (a *App) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeRawJSON(w, http.StatusOK, openAPISpec)
}

func (a *App) handleQRRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/qr" && r.URL.Path != "/wx/qrcodeauth" {
		writeError(w, http.StatusNotFound, "qr session not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	a.pruneQR()
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.RequestTimeout+35*time.Second)
	defer cancel()
	img, err := a.qr.GetQRCodeImage(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.mu.Lock()
	a.qrSessions[img.Session.ID] = img.Session
	keep := make(map[string]bool, len(a.qrSessions))
	for sid := range a.qrSessions {
		keep[sid] = true
	}
	a.mu.Unlock()
	path := a.resources.qrPath(img.Session.ID)
	_ = os.WriteFile(path, img.ImageBytes, 0o644)
	a.cleanupQR(keep)
	basePath := "/qr"
	if r.URL.Path == "/wx/qrcodeauth" {
		basePath = "/wx/qrcodeauth"
	}
	out := map[string]any{
		"session_id": img.Session.ID,
		"status":     img.Session.Status,
		"image_url":  basePath + "/" + img.Session.ID + "/image",
	}
	if r.URL.Query().Get("as_base64") == "true" {
		out["image_base64"] = qr.DataURIJPEG(img.ImageBytes)
	} else {
		out["image_base64"] = nil
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleQR(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/qr/")
	if path == r.URL.Path {
		path = strings.TrimPrefix(r.URL.Path, "/wx/qrcodeauth/")
	}
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		writeError(w, http.StatusNotFound, "qr session not found")
		return
	}
	sessionID, action := parts[0], parts[1]
	switch action {
	case "image":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		path := a.resources.qrPath(sessionID)
		if _, err := os.Stat(path); err != nil {
			writeError(w, http.StatusNotFound, "qr session not found")
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		http.ServeFile(w, r, path)
	case "poll":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		sess := a.getQRSession(sessionID)
		if sess == nil {
			writeError(w, http.StatusNotFound, "qr session not found")
			return
		}
		result, err := a.qr.PollQRCode(r.Context(), sess)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if terminalQR(result.Status) {
			a.dropQRSession(sessionID)
		}
		writeJSON(w, http.StatusOK, result)
	case "confirm":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		sess := a.getQRSession(sessionID)
		if sess == nil {
			writeError(w, http.StatusNotFound, "qr session not found")
			return
		}
		result, err := a.qr.GetLoginBuffer(r.Context(), sess)
		if err != nil {
			writeError(w, http.StatusConflict, "buffer not ready: "+err.Error())
			return
		}
		var userInfo map[string]any
		if ui, err := a.fetchUserInfo(r.Context(), result.Credentials); err == nil {
			userInfo = ui
		}
		acc, err := a.storeFromScan(r.Context(), r, result.LoginBuffer, result.Credentials, userInfo)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		a.dropQRSession(sessionID)
		writeJSON(w, http.StatusOK, acc.Public())
	default:
		writeError(w, http.StatusNotFound, "qr session not found")
	}
}

func (a *App) handleAccountsRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/accounts" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		accounts, err := a.listVisibleAccounts(r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]store.AccountPublic, 0, len(accounts))
		for _, acc := range accounts {
			out = append(out, acc.Public())
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodDelete:
		acc, ok := a.resolveAccountFromQuery(w, r)
		if !ok {
			return
		}
		cleanup, err := a.cleanupAccountFromQingLong(r.Context(), acc)
		if err != nil {
			writeError(w, http.StatusBadGateway, "青龙关联数据清理失败，本地账号未删除："+err.Error())
			return
		}
		if err := a.db.DeleteAccount(r.Context(), acc.ID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"deleted": acc.ID, "openid": acc.OpenID,
			"qinglong_cleanup": cleanup.Status, "env_entries_removed": cleanup.EnvEntriesRemoved,
			"tasks_deleted": cleanup.TasksDeleted,
		})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAccountAvatar(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/accounts/avatar" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	acc, ok := a.resolveAccountFromQuery(w, r)
	if !ok {
		return
	}
	a.serveAvatar(w, r, acc)
}

func (a *App) handleAccountRefresh(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/accounts/refresh" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body accountRefIn
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Ref == "" {
		a.refreshAll(w, r)
		return
	}
	acc, ok := a.resolveAccountRef(w, r, body.Ref)
	if !ok {
		return
	}
	status := a.refreshLiveness(r.Context(), acc)
	writeJSON(w, http.StatusOK, refreshOut(acc, status))
}

func (a *App) handleAccountResync(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/accounts/resync" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body accountRefIn
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Ref == "" {
		a.resyncAll(w, r)
		return
	}
	acc, ok := a.resolveAccountRef(w, r, body.Ref)
	if !ok {
		return
	}
	updated, err := a.resyncProfile(r.Context(), acc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated.Public())
}

func (a *App) handleGetCode(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/wxapp/getCode" && r.URL.Path != "/wx/code" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body wxappRequest
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	body.AppID = strings.TrimSpace(body.AppID)
	body.Ref = strings.TrimSpace(body.Ref)
	if body.AppID == "" {
		writeError(w, http.StatusBadRequest, "app_id is required")
		return
	}
	// Auto source selection: with a ref the native login_buffer protocol is
	// primary; without one the on-device WeChat hook is primary. Each path
	// transparently falls back to the other source on failure.
	var (
		result   map[string]any
		openid   string
		source   string
		fallback bool
	)
	if body.Ref != "" {
		acc, err := a.resolveReadableAccount(r, body.Ref)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		nat, err := a.invokeWXApp(r.Context(), acc, body.AppID, body.Payload, a.invokeGetCode)
		if err == nil {
			result, openid, source, fallback = nat, acc.OpenID, "native", false
		} else {
			primaryErr := err
			if dev, devErr := a.deviceCodeResult(r.Context(), body.AppID); devErr == nil {
				result, source, fallback = dev, "device", true
			} else {
				writeNativeCallError(w, primaryErr)
				return
			}
		}
	} else {
		dev, err := a.deviceCodeResult(r.Context(), body.AppID)
		if err == nil {
			result, source, fallback = dev, "device", false
		} else {
			primaryErr := err
			if nat, oid, natErr := a.nativeCodeWithRef(r, "", body.AppID); natErr == nil {
				result, openid, source, fallback = nat, oid, "native", true
			} else {
				writeError(w, http.StatusBadGateway, "device failed: "+primaryErr.Error()+"; native failed: "+natErr.Error())
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"openid": openid, "source": source, "fallback": fallback, "result": result,
	})
}

func (a *App) handleWXCodeAlias(w http.ResponseWriter, r *http.Request) {
	a.handleGetCode(w, r)
}

// writeNativeCallError preserves the original error semantics of the native
// login_buffer path for /wxapp/getCode callers.
func writeNativeCallError(w http.ResponseWriter, err error) {
	var expired accountExpiredError
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "account not found")
	case errors.As(err, &expired):
		writeError(w, http.StatusConflict, "account login_buffer expired (refresh failed); re-scan required")
	default:
		writeError(w, http.StatusBadGateway, "call failed: "+err.Error())
	}
}

func (a *App) handleGetPhoneNumber(w http.ResponseWriter, r *http.Request) {
	if !acceptWXAppRoute(w, r, "/wxapp/getPhoneNumber") {
		return
	}
	a.callWXApp(w, r, false, a.invokeGetPhoneNumber)
}

func (a *App) handleWXPhoneAlias(w http.ResponseWriter, r *http.Request) {
	if !acceptWXAppRoute(w, r, "/wx/getphonenumber") {
		return
	}
	a.callWXApp(w, r, false, a.invokeGetPhoneNumber)
}

func (a *App) handleOperateWXData(w http.ResponseWriter, r *http.Request) {
	if !acceptWXAppRoute(w, r, "/wxapp/operateWxData") {
		return
	}
	a.callWXApp(w, r, true, a.invokeOperateWXData)
}

func (a *App) handleWXEncryptKey(w http.ResponseWriter, r *http.Request) {
	a.handleNamedWXOperation(w, r, "/wx/encryptkey", "getUserEncryptKey", false)
}

func (a *App) handleWXCloud(w http.ResponseWriter, r *http.Request) {
	a.handleNamedWXOperation(w, r, "/wx/cloud", "cloud.callFunction", true)
}

func (a *App) handleWXMPGetA8Key(w http.ResponseWriter, r *http.Request) {
	a.handleNamedWXOperation(w, r, "/wx/mpgeta8key", "mpGetA8Key", true)
}

func (a *App) handleWXAppMsgExt(w http.ResponseWriter, r *http.Request) {
	a.handleNamedWXOperation(w, r, "/wx/appmsgext", "appmsgext", true)
}

func (a *App) handleWXAppMsgLike(w http.ResponseWriter, r *http.Request) {
	a.handleNamedWXOperation(w, r, "/wx/appmsglike", "appmsglike", true)
}

// handleDeviceGetCode obtains a wx.login code from the on-device wxcode
// Xposed service (WXCODE_URLS, default http://127.0.0.1:8088) instead of the
// server-side login_buffer protocol. Accepts POST JSON {"app_id": "..."} or
// GET /wxapp/deviceCode?app_id=... and returns the same envelope shape as
// /wxapp/getCode so existing automation scripts can switch sources by only
// changing the endpoint.
func (a *App) handleDeviceGetCode(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path != "/wxapp/deviceCode" && path != "/wx/devicecode" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		AppID string `json:"app_id"`
	}
	if r.Method == http.MethodPost {
		if err := decodeOptionalJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
	} else {
		body.AppID = r.URL.Query().Get("app_id")
	}
	body.AppID = strings.TrimSpace(body.AppID)
	if body.AppID == "" {
		writeError(w, http.StatusBadRequest, "app_id is required")
		return
	}
	if a.wxcode == nil || !a.wxcode.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "no wxcode device endpoints configured (set WXCODE_URLS)")
		return
	}
	result, err := a.wxcode.GetCode(r.Context(), body.AppID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "device code source failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source": "device",
		"result": map[string]any{
			"appId":      result.AppID,
			"status":     result.Status,
			"code":       result.Code,
			"codeType":   result.CodeType,
			"codeLength": result.CodeLength,
		},
	})
}

// handleWXCodeStatus reports the health of every configured wxcode device
// endpoint for the console status widget.
func (a *App) handleWXCodeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.wxcode == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "endpoints": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":   a.wxcode.Enabled(),
		"endpoints": a.wxcode.Status(r.Context()),
	})
}

// handleNamedWXOperation adapts named /wx/* compatibility calls to the
// generic operateWxData transport. Callers may provide a complete payload;
// otherwise a minimal api_name/data envelope is generated.
func (a *App) handleNamedWXOperation(w http.ResponseWriter, r *http.Request, path, apiName string, requirePayload bool) {
	if !acceptWXAppRoute(w, r, path) {
		return
	}
	var body wxappRequest
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.AppID == "" {
		writeError(w, http.StatusBadRequest, "app_id is required")
		return
	}
	acc, ok := a.resolveAPIAccountRef(w, r, body.Ref)
	if !ok {
		return
	}
	if body.Payload == nil {
		if requirePayload {
			writeError(w, http.StatusBadRequest, "payload is required for "+apiName)
			return
		}
		body.Payload = map[string]any{"api_name": apiName, "data": map[string]any{}, "env": 1}
	}
	result, err := a.invokeNamedWXOperation(r.Context(), acc, body, apiName)
	if err != nil {
		var expired accountExpiredError
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "account not found: "+body.Ref)
			return
		case errors.As(err, &expired):
			writeError(w, http.StatusConflict, "account login_buffer expired (refresh failed); re-scan required")
			return
		case strings.Contains(err.Error(), " is required"):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "call failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) invokeNamedWXOperation(ctx context.Context, acc *store.WechatAccount, body wxappRequest, apiName string) (map[string]any, error) {
	if body.Payload == nil {
		body.Payload = map[string]any{"api_name": apiName, "data": map[string]any{}, "env": 1}
	}
	result, err := a.invokeWXApp(ctx, acc, body.AppID, body.Payload, a.invokeOperateWXData)
	if err != nil {
		return nil, err
	}
	return map[string]any{"openid": acc.OpenID, "result": result}, nil
}

func (a *App) handleWXGetUserInfo(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/wx/getuserinfo" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body accountRefIn
	if r.Method == http.MethodPost {
		if err := decodeOptionalJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
	} else {
		body.Ref = r.URL.Query().Get("ref")
	}
	acc, ok := a.resolveAPIAccountRef(w, r, body.Ref)
	if !ok {
		return
	}
	if len(acc.Credentials) == 0 {
		writeError(w, http.StatusConflict, "account has no login credentials")
		return
	}
	creds := protocol.CredentialsFromMap(acc.Credentials)
	info, err := a.fetchUserInfo(r.Context(), creds)
	if err != nil {
		if status := a.refreshLiveness(r.Context(), acc); status == "alive" {
			if fresh, getErr := a.db.GetAccount(r.Context(), acc.ID); getErr == nil {
				acc = fresh
				info, err = a.fetchUserInfo(r.Context(), protocol.CredentialsFromMap(acc.Credentials))
			}
		}
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "getuserinfo failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"openid": acc.OpenID, "user_info": info})
}

func acceptWXAppRoute(w http.ResponseWriter, r *http.Request, path string) bool {
	if r.URL.Path != path {
		writeError(w, http.StatusNotFound, "not found")
		return false
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	return true
}

type accountRefIn struct {
	Ref string `json:"ref"`
}

type wxappRequest struct {
	Ref     string         `json:"ref"`
	AppID   string         `json:"app_id"`
	Payload map[string]any `json:"payload"`
}

type wxappCall func(ctx context.Context, acc *store.WechatAccount, appID string, payload map[string]any) (map[string]any, error)

func (a *App) callWXApp(w http.ResponseWriter, r *http.Request, requirePayload bool, call wxappCall) {
	var body wxappRequest
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Ref == "" {
		writeError(w, http.StatusBadRequest, "ref is required")
		return
	}
	if body.AppID == "" {
		writeError(w, http.StatusBadRequest, "app_id is required")
		return
	}
	if requirePayload && body.Payload == nil {
		writeError(w, http.StatusBadRequest, "payload is required")
		return
	}
	acc, ok := a.resolveAPIAccountRef(w, r, body.Ref)
	if !ok {
		return
	}
	result, err := a.invokeWXApp(r.Context(), acc, body.AppID, body.Payload, call)
	if err != nil {
		var expired accountExpiredError
		switch {
		case errors.As(err, &expired):
			writeError(w, http.StatusConflict, "account login_buffer expired (refresh failed); re-scan required")
		default:
			writeError(w, http.StatusBadGateway, "call failed: "+err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"openid": acc.OpenID, "result": result})
}

func decodeOptionalJSON(r *http.Request, dst any) error {
	err := json.NewDecoder(r.Body).Decode(dst)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// 账号解析（resolveAccountRef / resolveAccountFromQuery / resolveAPIAccountRef）
// 统一放在 access.go，便于集中处理归属与“允许脚本读取”开关。

func (a *App) refreshAll(w http.ResponseWriter, r *http.Request) {
	accounts, err := a.listVisibleAccounts(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(accounts))
	for _, acc := range accounts {
		out = append(out, refreshOut(acc, a.refreshLiveness(r.Context(), acc)))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) resyncAll(w http.ResponseWriter, r *http.Request) {
	accounts, err := a.listVisibleAccounts(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]store.AccountPublic, 0, len(accounts))
	for _, acc := range accounts {
		updated, err := a.resyncProfile(r.Context(), acc)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, updated.Public())
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) serveAvatar(w http.ResponseWriter, r *http.Request, acc *store.WechatAccount) {
	if acc.Avatar != nil && *acc.Avatar != "" {
		if _, err := os.Stat(*acc.Avatar); err == nil {
			w.Header().Set("Content-Type", "image/jpeg")
			http.ServeFile(w, r, *acc.Avatar)
			return
		}
		if strings.HasPrefix(*acc.Avatar, "http://") || strings.HasPrefix(*acc.Avatar, "https://") {
			http.Redirect(w, r, *acc.Avatar, http.StatusFound)
			return
		}
	}
	writeError(w, http.StatusNotFound, "no avatar")
}

// storeFromScan 落库扫码/快捷登录拿到的账号，并按当前登录用户写入归属。
func (a *App) storeFromScan(ctx context.Context, r *http.Request, loginBuffer string, creds protocol.LoginBufferCredentials, userInfo map[string]any) (*store.WechatAccount, error) {
	openid := creds.OpenID
	nick := pickNickname(userInfo, creds.Nickname)
	avatar := a.resolveAvatar(ctx, openid, userInfo)
	status := "alive"
	owner := a.ownerForScan(ctx, r, openid)
	return a.db.UpsertAccount(ctx, openid, loginBuffer, stringPtrMaybe(nick), stringPtrMaybe(nick), stringPtrMaybe(avatar), userInfo, creds.ToMap(), &status, owner)
}

func (a *App) resyncProfile(ctx context.Context, acc *store.WechatAccount) (*store.WechatAccount, error) {
	nick := pickNickname(acc.UserInfo, deref(acc.Nickname))
	avatar := a.resolveAvatar(ctx, acc.OpenID, acc.UserInfo)
	if avatar == "" {
		avatar = deref(acc.Avatar)
	}
	if err := a.db.SetAccountProfile(ctx, acc.ID, stringPtrMaybe(nick), stringPtrMaybe(avatar), acc.UserInfo); err != nil {
		return nil, err
	}
	return a.db.GetAccount(ctx, acc.ID)
}

type accountExpiredError struct{ openid string }

func (e accountExpiredError) Error() string { return "account expired: " + e.openid }

func (a *App) invokeWXApp(ctx context.Context, acc *store.WechatAccount, appID string, payload map[string]any, call wxappCall) (map[string]any, error) {
	proxy := a.cfg.TCPProxy
	if _, err := a.db.GetSession(ctx, acc.ID, proxy); err == nil {
		result, err := call(ctx, acc, appID, payload)
		if err == nil {
			return result, nil
		}
		_ = a.db.InvalidateSession(ctx, acc.ID, proxy)
	}
	status := a.refreshLiveness(ctx, acc)
	if status != "alive" {
		return nil, accountExpiredError{openid: acc.OpenID}
	}
	fresh, err := a.db.GetAccount(ctx, acc.ID)
	if err == nil && fresh != nil {
		acc = fresh
	}
	return call(ctx, acc, appID, payload)
}

func (a *App) invokeGetCode(ctx context.Context, acc *store.WechatAccount, appID string, _ map[string]any) (map[string]any, error) {
	return a.pool.GetCode(ctx, acc.LoginBuffer, appID, acc.ID, a.cfg.TCPProxy)
}

func (a *App) invokeGetPhoneNumber(ctx context.Context, acc *store.WechatAccount, appID string, _ map[string]any) (map[string]any, error) {
	return a.pool.GetPhoneNumber(ctx, acc.LoginBuffer, appID, acc.ID, a.cfg.TCPProxy)
}

func (a *App) invokeOperateWXData(ctx context.Context, acc *store.WechatAccount, appID string, payload map[string]any) (map[string]any, error) {
	return a.pool.OperateWXData(ctx, acc.LoginBuffer, appID, payload, acc.ID, a.cfg.TCPProxy)
}

func refreshOut(acc *store.WechatAccount, status string) map[string]any {
	return map[string]any{"id": acc.ID, "openid": acc.OpenID, "uin": acc.UIN, "nickname": acc.Nickname, "status": status}
}

func pickNickname(userInfo map[string]any, fallback string) string {
	if s := stringFromAny(userInfo["nick_name"]); s != "" {
		return s
	}
	return fallback
}

func pickAvatarURL(userInfo map[string]any) string {
	for _, k := range []string{"head_img_url", "head_url", "headimgurl", "avatar"} {
		if s := stringFromAny(userInfo[k]); s != "" {
			return s
		}
	}
	return ""
}

func (a *App) resolveAvatar(ctx context.Context, openid string, userInfo map[string]any) string {
	u := pickAvatarURL(userInfo)
	if u == "" {
		return ""
	}
	dest := a.resources.avatarPath(openid)
	if downloadAvatar(ctx, u, dest, a.cfg.AvatarTimeout) {
		return dest
	}
	return u
}

func downloadAvatar(ctx context.Context, url, dest string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || resp.StatusCode != 200 || !looksLikeImage(data) {
		return false
	}
	_ = os.MkdirAll(filepath.Dir(dest), 0o755)
	return os.WriteFile(dest, data, 0o644) == nil
}

func looksLikeImage(data []byte) bool {
	if len(data) < 64 {
		return false
	}
	magics := [][]byte{{0xff, 0xd8, 0xff}, {0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("GIF87a"), []byte("GIF89a")}
	for _, m := range magics {
		if strings.HasPrefix(string(data), string(m)) {
			return true
		}
	}
	return false
}

func (a *App) getQRSession(id string) *qr.Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.qrSessions[id]
}

func (a *App) dropQRSession(id string) {
	a.mu.Lock()
	delete(a.qrSessions, id)
	a.mu.Unlock()
	_ = os.Remove(a.resources.qrPath(id))
}

func (a *App) pruneQR() {
	a.mu.Lock()
	var drop []string
	for sid, sess := range a.qrSessions {
		if sess.Age() > a.cfg.QRSessionTTL {
			drop = append(drop, sid)
		}
	}
	for _, sid := range drop {
		delete(a.qrSessions, sid)
	}
	a.mu.Unlock()
	for _, sid := range drop {
		_ = os.Remove(a.resources.qrPath(sid))
	}
}

func (a *App) cleanupQR(keep map[string]bool) {
	files, _ := filepath.Glob(filepath.Join(a.resources.QR, "*.jpg"))
	for _, f := range files {
		sid := strings.TrimSuffix(filepath.Base(f), ".jpg")
		if !keep[sid] {
			_ = os.Remove(f)
		}
	}
}

func terminalQR(status string) bool {
	return status == "expired" || status == "cancelled" || status == "unknown"
}

type apiEnvelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	writeRawJSON(w, status, apiEnvelope{
		Code: 0,
		Msg:  "success",
		Data: v,
	})
}

func writeRawJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, detail string) {
	writeRawJSON(w, status, apiEnvelope{
		Code: status,
		Msg:  detail,
		Data: nil,
	})
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

func serveFileOrText(w http.ResponseWriter, r *http.Request, path, fallback string) {
	if _, err := os.Stat(path); err == nil {
		http.ServeFile(w, r, path)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(fallback))
}

func stringFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func stringPtrMaybe(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sortedKeys[M ~map[string]V, V any](m M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

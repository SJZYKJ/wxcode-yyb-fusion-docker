package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"yyb_go/internal/httpapi"
	"yyb_go/internal/protocol"
)

func main() {
	envFile := flag.String("env-file", "", "dotenv file; defaults to .env beside executable, then working directory")
	host := flag.String("host", "127.0.0.1", "listen host")
	port := flag.Int("port", 8088, "listen port")
	resourceRoot := flag.String("resource-root", filepath.Join(".", "resource"), "runtime resource directory")
	dbFilename := flag.String("db", httpapi.DefaultDBFilename, "SQLite database filename under resource/db")
	tcpProxy := flag.String("tcp-proxy", "", "optional TCP proxy: socks5://host:port or http-connect://host:port")
	keepAliveInterval := flag.Duration("keepalive-interval", 2*time.Minute, "account keepalive check interval; 0 disables")
	keepAliveAhead := flag.Duration("keepalive-ahead", 4*time.Minute, "refresh credentials this long before expiry")
	resetAdmin := flag.String("reset-admin-password", "", "reset an existing administrator's password locally, then exit; password is read from stdin")
	// SMTP：注册邮箱验证码的发信配置。命令行 flag 优先，缺省回落到
	// YYB_SMTP_* 环境变量；未配置时 /api/auth/email-code 返回 503，
	// 且注册会被明确拒绝（不允许静默跳过邮箱验证）。
	smtpHost := flag.String("smtp-host", "", "SMTP host for registration email codes (env YYB_SMTP_HOST)")
	smtpPort := flag.Int("smtp-port", 0, "SMTP port, default 465 (env YYB_SMTP_PORT)")
	smtpUsername := flag.String("smtp-username", "", "SMTP username (env YYB_SMTP_USERNAME)")
	smtpPassword := flag.String("smtp-password", "", "SMTP password (env YYB_SMTP_PASSWORD)")
	smtpFrom := flag.String("smtp-from", "", "SMTP envelope/From address (env YYB_SMTP_FROM)")
	smtpTLS := flag.Bool("smtp-tls", true, "true: implicit TLS/SMTPS (465); false: STARTTLS (env YYB_SMTP_TLS)")
	flag.Parse()
	executable, executableErr := os.Executable()
	executableDir := ""
	if executableErr == nil {
		executableDir = filepath.Dir(executable)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		log.Fatal("无法读取当前工作目录")
	}
	if loaded, err := loadEnvFile(*envFile, executableDir, workingDir); err != nil {
		log.Fatal(err)
	} else if loaded != "" {
		log.Printf("loaded configuration: %s", loaded)
	}
	if err := applyEnvFlags(flag.CommandLine); err != nil {
		log.Fatal(err)
	}
	resourceRootExplicit := false
	flag.Visit(func(current *flag.Flag) {
		if current.Name == "resource-root" {
			resourceRootExplicit = true
		}
	})
	panelType := getEnvWithFallback("PANEL_TYPE", "YYB_PANEL_TYPE", "QL_TYPE")
	if panelType == "" {
		panelType = "qinglong"
	}

	var panelURL string
	if panelType == "daidai" {
		daidaiURL := getEnvWithFallback("DAIDAI_URL")
		qlURL := getEnvWithFallback("QL_URL")
		if daidaiURL != "" {
			panelURL = daidaiURL
		} else if qlURL != "" && qlURL != "http://qinglong:5700" {
			panelURL = qlURL
		} else {
			panelURL = "http://daidai-panel:5700"
		}
	} else {
		qlURL := getEnvWithFallback("QL_URL")
		daidaiURL := getEnvWithFallback("DAIDAI_URL")
		if qlURL != "" {
			panelURL = qlURL
		} else if daidaiURL != "" {
			panelURL = daidaiURL
		} else {
			panelURL = "http://qinglong:5700"
		}
	}

	var clientID, clientSecret string
	if panelType == "daidai" {
		clientID = getEnvWithFallback("DAIDAI_APP_KEY", "QL_CLIENT_ID")
		clientSecret = getEnvWithFallback("DAIDAI_APP_SECRET", "QL_CLIENT_SECRET")
	} else {
		clientID = getEnvWithFallback("QL_CLIENT_ID", "DAIDAI_APP_KEY")
		clientSecret = getEnvWithFallback("QL_CLIENT_SECRET", "DAIDAI_APP_SECRET")
	}

	legacyAuthDSN := os.Getenv("YYB_AUTH_MYSQL_DSN")
	authDriver := strings.ToLower(strings.TrimSpace(os.Getenv("YYB_AUTH_DRIVER")))
	if authDriver == "" {
		if legacyAuthDSN != "" {
			authDriver = "mysql"
		} else {
			authDriver = "sqlite"
		}
	}

	// SMTP 配置：先读环境变量（非法值直接启动失败，而不是带着错误配置跑起来），
	// 再用显式传入的 flag 覆盖。
	smtpCfg, smtpErr := httpapi.ParseSMTPConfig(func(key string) string { return os.Getenv(key) })
	if smtpErr != nil {
		log.Fatalf("configure SMTP: %v", smtpErr)
	}
	if *smtpHost != "" {
		smtpCfg.Host = *smtpHost
	}
	if *smtpPort != 0 {
		smtpCfg.Port = *smtpPort
	}
	if *smtpUsername != "" {
		smtpCfg.Username = *smtpUsername
	}
	if *smtpPassword != "" {
		smtpCfg.Password = *smtpPassword
	}
	if *smtpFrom != "" {
		smtpCfg.From = *smtpFrom
	}
	if !*smtpTLS {
		smtpCfg.TLS = false
	}
	if smtpCfg.Configured() {
		log.Printf("注册邮箱验证码已启用：SMTP %s:%d（TLS=%v，发件人 %s）", smtpCfg.Host, smtpCfg.Port, smtpCfg.TLS, smtpCfg.From)
	} else {
		log.Printf("注册邮箱验证码未启用：未配置 SMTP（YYB_SMTP_HOST/PORT/USERNAME/PASSWORD/FROM），注册将被拒绝并提示联系管理员")
	}

	cfg := httpapi.Config{
		ResourceRoot:      *resourceRoot,
		EmbeddedWebAssets: !resourceRootExplicit,
		DBFilename:        *dbFilename,
		TCPProxy:          *tcpProxy,
		SessionTTL:        30 * time.Minute,
		RequestTimeout:    8 * time.Second,
		AvatarTimeout:     10 * time.Second,
		ScanTimeout:       180 * time.Second,
		QRSessionTTL:      5 * time.Minute,
		KeepAliveInterval: *keepAliveInterval,
		KeepAliveAhead:    *keepAliveAhead,
		ASRefreshMode:     os.Getenv("YYB_APPSTORE_REFRESH_MODE"),
		DeviceIdentity:    os.Getenv("YYB_DEVICE_IDENTITY"),
		QingLongType:      panelType,
		QingLongURL:       panelURL,
		QingLongClientID:  clientID,
		QingLongSecret:    clientSecret,
		Port:              *port,
		QingLongServer:    os.Getenv("YYB_QINGLONG_SERVER"),
		AuthDriver:        authDriver,
		AuthDSN:           os.Getenv("YYB_AUTH_DSN"),
		AuthMySQLDSN:      legacyAuthDSN,
		AdminUser:         getEnvWithFallback("YYB_ADMIN_USER", "YYB_WEB_USER"),
		AdminPassword:     getEnvWithFallback("YYB_ADMIN_PASSWORD", "YYB_WEB_PASSWORD"),
		// 零配置部署：没配管理员密码、也没配 SMTP 时，注册页用不了，
		// 这里自动建一个 admin 并把随机密码打到日志，保证首次能进控制台。
		AutoBootstrapAdmin: true,
		CookieSecure:       os.Getenv("YYB_COOKIE_SECURE") == "true",
		EnablePCLogin:      strings.EqualFold(strings.TrimSpace(os.Getenv("YYB_ENABLE_PC_LOGIN")), "true"),

		// Accounts with a script push channel are notified there by default.
		NotifyChannel:            os.Getenv("YYB_NOTIFY_CHANNEL"),
		NotifyToken:              os.Getenv("YYB_NOTIFY_TOKEN"),
		NotifyTopic:              os.Getenv("YYB_NOTIFY_TOPIC"),
		DisableAccountPushNotify: strings.EqualFold(strings.TrimSpace(os.Getenv("YYB_NOTIFY_ACCOUNT_PUSH")), "false"),

		DisableCircuitBreaker: strings.EqualFold(strings.TrimSpace(os.Getenv("YYB_CIRCUIT_BREAKER")), "off"),

		// Upstream requests of one account are spaced by default. The breaker and
		// the login budget only react to failures and OAuth calls, so a burst of
		// successful codes would pass both.
		CodeMinGap:  envDuration("YYB_CODE_MIN_GAP", defaultCodeMinGap),
		CodeMaxWait: envDuration("YYB_CODE_MAX_WAIT", 20*time.Second),
		LoginBudget: envInt("YYB_LOGIN_BUDGET", protocol.DefaultLoginBudget),
		LoginWindow: envDuration("YYB_LOGIN_WINDOW", protocol.DefaultLoginWindow),

		// A -14 shortly after a success is not repaired with a new login. On
		// accounts whose session serves one code that refuses the repair that
		// would work, so the window can be shortened or switched off.
		FreshSessionWindow: envFreshSessionWindow("YYB_FRESH_SESSION_WINDOW"),

		// Website-application AppIDs never get a code and cost the account its
		// authorization, so code requests refuse the known ones by default.
		CodeDenyAppIDs:  envList("YYB_CODE_DENY_APPIDS"),
		CodeAllowAppIDs: envList("YYB_CODE_ALLOW_APPIDS"),

		// An AppID whose code request reaches the upstream and comes back
		// without a code is learned as dangerous and denied for every account;
		// YYB_CODE_DYNAMIC_DENY=0 switches the learning off.
		DisableDynamicAppIDDeny: !envBool("YYB_CODE_DYNAMIC_DENY", true),

		// How many distinct accounts must fail an AppID before it is denied
		// (or dynamicAppIDDenyTotalStrikes failures in total); default is 2,
		// so one bursted account cannot deny AppIDs other accounts still use.
		CodeDynamicDenyThreshold: envInt("YYB_CODE_DYNAMIC_DENY_THRESHOLD", 2),

		APIToken:          strings.TrimSpace(os.Getenv("YYB_API_TOKEN")),
		AllowNoAuth:       envBool("YYB_ALLOW_NO_AUTH"),
		AllowRegistration: envBool("YYB_ALLOW_REGISTRATION"),
		TrustProxy:        envBool("YYB_TRUST_PROXY"),
		SMTP:              smtpCfg,

		// 上游 0.3.x 新增：协议路由令牌、集成接口令牌、青龙脚本仓库与账号 ref 模式。
		ProtocolToken:    os.Getenv("YYB_PROTOCOL_TOKEN"),
		IntegrationToken: os.Getenv("YYB_INTEGRATION_TOKEN"),
		QingLongRepo:     os.Getenv("YYB_QINGLONG_REPO"),
		QingLongRefMode:  getEnvWithFallback("YYB_QINGLONG_REF_MODE", "YYB_ACCOUNT_REF_MODE"),
	}

	if *resetAdmin != "" {
		if err := resetAdminPassword(cfg, *resetAdmin, os.Stdin, os.Stdout); err != nil {
			log.Fatalf("重置失败：%v", err)
		}
		return
	}
	if dnsServers, err := configureDNS(os.Getenv("YYB_DNS_SERVERS")); err != nil {
		log.Fatalf("configure DNS: %v", err)
	} else if len(dnsServers) > 0 {
		log.Printf("using configured DNS servers: %s", strings.Join(dnsServers, ", "))
	}
	app, err := httpapi.NewApp(cfg)
	if err != nil {
		log.Fatalf("init app: %v", err)
	}
	defer app.Close()

	addr := fmt.Sprintf("%s:%d", *host, *port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("wxcode-YYB service listening on http://%s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func splitListEnv(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// defaultCodeMinGap is the least time between two upstream requests of one
// account. It is a conservative starting point, not a measured safe rate: the
// one burst that got an authorization rejected after successful requests alone
// came in at about 1.6 requests a second.
const defaultCodeMinGap = 3 * time.Second

// envList reads a list separated by commas or white space. An unset or empty
// value gives no entries.
func envList(key string) []string {
	return strings.FieldsFunc(os.Getenv(key), func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
}

// envDuration reads a Go duration such as "2s". "off" and "0" mean zero, which
// switches the setting off. An empty or unreadable value keeps the default and
// says so, instead of silently running with a different value.
func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if strings.EqualFold(raw, "off") {
		return 0
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < 0 {
		log.Printf("%s=%q is not a valid duration; using %s", key, raw, fallback)
		return fallback
	}
	return value
}

// envFreshSessionWindow reads the fresh-session guard window: unset keeps the
// default, 0 or off switches the guard off (a negative Config value), anything
// else is the window.
func envFreshSessionWindow(key string) time.Duration {
	window := envDuration(key, protocol.DefaultFreshSessionWindow)
	if window == 0 {
		return -1
	}
	return window
}

// envInt reads a whole number. "off" means zero, which switches the setting
// off; an unreadable value keeps the default and says so.
func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if strings.EqualFold(raw, "off") {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		log.Printf("%s=%q is not a valid whole number; using %d", key, raw, fallback)
		return fallback
	}
	return value
}

func getEnvWithFallback(keys ...string) string {
	for _, key := range keys {
		if val := os.Getenv(key); val != "" {
			return val
		}
	}
	return ""
}

// envBool 读取布尔型环境变量：1/true/yes/on 为真，0/false/off/no 为假（大小写不敏感）。
// 未设置或值不可识别时返回 fallback；未提供 fallback 时默认 false——安全相关的开关
// 一律默认关闭。保留可选的 fallback 参数是为了同时兼容上游 envBool(key, fallback)
// 与下游 envBool(key) 两种调用方式（Go 不支持函数重载，合并时统一为一个函数）。
func envBool(key string, fallback ...bool) bool {
	def := false
	if len(fallback) > 0 {
		def = fallback[0]
	}
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	switch strings.ToLower(raw) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	default:
		log.Printf("%s=%q is not a valid switch; using %v", key, raw, def)
		return def
	}
}

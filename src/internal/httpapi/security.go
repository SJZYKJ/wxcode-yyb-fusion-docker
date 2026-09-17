package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net"
	"net/http"
	"strings"
)

// 本文件集中放置"安全默认值"相关的判定：访问令牌的解析、可信客户端 IP、
// 内网地址判定。改动这些函数会直接影响暴露面，请配合 api_token.go 一起阅读。

// apiTokenSetting 是自动生成令牌在数据库 app_settings 表中的键。
// 令牌落库而不是落文件，是为了跟随数据卷持久化：容器重建/镜像升级后
// 令牌保持不变，青龙脚本里的 YYB_API_TOKEN 不需要跟着改。
const apiTokenSetting = "api_token"

// resolveAPIToken 决定本次启动实际生效的取码接口访问令牌（fail-closed）。
//
// 解析顺序：
//  1. 显式配置的 YYB_API_TOKEN —— 优先级最高，值就是它；
//  2. 数据库里上次自动生成的令牌 —— 重启后保持稳定；
//  3. 都没有 —— 生成一个新的 256bit 随机令牌、写入数据库并打印到日志。
//
// 唯一能让取码接口「不做鉴权」的方式，是运维显式设置
// YYB_ALLOW_NO_AUTH=true|1|yes。返回的 generated 表示本次是否新生成。
func resolveAPIToken(ctx context.Context, db interface {
	GetSetting(context.Context, string) (string, error)
	SetSetting(context.Context, string, string) error
}, configured string, allowNoAuth bool) (token string, generated bool) {
	if value := strings.TrimSpace(configured); value != "" {
		return value, false
	}
	if allowNoAuth {
		return "", false
	}
	if stored, err := db.GetSetting(ctx, apiTokenSetting); err == nil {
		if value := strings.TrimSpace(stored); value != "" {
			return value, false
		}
	}
	token = randomHexToken(32)
	if err := db.SetSetting(ctx, apiTokenSetting, token); err != nil {
		log.Printf("[安全] 自动生成的 API 令牌无法写回数据库（重启后可能变化）：%v", err)
	}
	return token, true
}

// randomHexToken 生成 n 字节密码学随机数的十六进制串；失败时 panic，
// 因为"拿不到随机数还继续启动"会让访问令牌退化成可预测值。
func randomHexToken(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(buf)
}

// announceAPIToken 把生效的鉴权模式打到日志里。令牌本身会被打印，
// 因为零配置部署的用户只能从 docker logs 里取到它。
func announceAPIToken(token string, generated, allowNoAuth bool) {
	switch {
	case allowNoAuth:
		log.Printf("[安全警告] YYB_ALLOW_NO_AUTH 已开启：/login、/instances、/wxapp/*、/wx/* 等取码接口【不做任何鉴权】，")
		log.Printf("[安全警告] 端口一旦可达，任何人都能列出全部 openid 并取到 code。请仅在纯内网/已由反代鉴权时使用。")
	case generated:
		log.Printf("==========================================================================")
		log.Printf(" 已自动生成 API 访问令牌（未配置 YYB_API_TOKEN）")
		log.Printf(" 令牌: %s", token)
		log.Printf(" 取码接口现在需要鉴权，请把上面的令牌填到：")
		log.Printf("   - 青龙环境变量 YYB_API_TOKEN（三个脚本都会带 Authorization: Bearer）")
		log.Printf("   - 或调用时携带 X-API-Token / ?token=  请求头/参数")
		log.Printf(" 令牌已存入数据库，容器重启/升级后保持不变。")
		log.Printf("==========================================================================")
	default:
		log.Printf("API 访问令牌已启用：/login、/instances、/wxapp/*、/wx/* 等取码接口需要")
		log.Printf("Bearer / X-API-Token / ?token= 令牌，或一个有效的控制台登录会话。")
	}
}

// isLocalOrPrivate 判断请求来源是否是内网/回环/CGNAT 地址。
// 用于「首次部署引导窗口」：只有内网访问才允许在没有任何账号时注册管理员，
// 公网直连一律拒绝，避免实例刚暴露就被陌生人抢注成管理员。
func isLocalOrPrivate(raw string) bool {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return true
	}
	// 100.64.0.0/10（运营商 CGNAT），NAS 走运营商大内网时会出现。
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}

// firstForwardedFor 只取 X-Forwarded-For 的第一段（最靠近客户端的地址）。
func firstForwardedFor(r *http.Request) string {
	raw := r.Header.Get("X-Forwarded-For")
	if raw == "" {
		return ""
	}
	return strings.TrimSpace(strings.Split(raw, ",")[0])
}

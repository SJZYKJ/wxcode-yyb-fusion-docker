package httpapi

import (
	"context"
	"crypto/rand"
	"log"
	"math/big"
	"strings"

	"yyb_go/internal/auth"
)

// bootstrapAdminPasswordAlphabet 去掉容易看错的 0/O/1/l/I，方便从日志里照抄。
const bootstrapAdminPasswordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// bootstrapAdminPasswordLength 16 位随机密码，满足「至少 10 个字符」的校验。
const bootstrapAdminPasswordLength = 16

// defaultBootstrapAdminUser 是零配置部署时的管理员账号名（可用 YYB_ADMIN_USER 覆盖）。
const defaultBootstrapAdminUser = "admin"

// generateBootstrapAdminPassword 用 crypto/rand 生成初始管理员密码。
// 随机数不可用时返回空串，调用方会退回「不自动建账号」，绝不退化成固定密码。
func generateBootstrapAdminPassword() string {
	var builder strings.Builder
	maxValue := big.NewInt(int64(len(bootstrapAdminPasswordAlphabet)))
	for i := 0; i < bootstrapAdminPasswordLength; i++ {
		index, err := rand.Int(rand.Reader, maxValue)
		if err != nil {
			return ""
		}
		builder.WriteByte(bootstrapAdminPasswordAlphabet[index.Int64()])
	}
	return builder.String()
}

// bootstrapAdminAccount 处理启动时的首个管理员账号。
//
// 三种情况：
//  1. 配了 YYB_ADMIN_PASSWORD —— 老行为，按配置创建（库非空时内部会跳过）；
//  2. 没配密码、也没开 AutoBootstrapAdmin —— 什么都不做（测试与嵌入式用法）；
//  3. 没配密码但开了 AutoBootstrapAdmin（main 默认开）—— 库为空时生成随机密码
//     创建管理员并打印到日志，保证「没配 SMTP 也能先进控制台」。
func bootstrapAdminAccount(ctx context.Context, store *auth.Store, cfg Config) error {
	password := strings.TrimSpace(cfg.AdminPassword)
	if password != "" {
		return store.BootstrapAdmin(ctx, cfg.AdminUser, password)
	}
	if !cfg.AutoBootstrapAdmin {
		return nil
	}
	username := strings.TrimSpace(cfg.AdminUser)
	if username == "" {
		// 零配置部署不设 YYB_ADMIN_USER，这里退回文档里写的默认账号名。
		username = defaultBootstrapAdminUser
	}
	generated := generateBootstrapAdminPassword()
	if generated == "" {
		log.Printf("[账号] 无法生成随机管理员密码（crypto/rand 不可用），跳过自动创建")
		return nil
	}
	created, err := store.BootstrapAdminIfEmpty(ctx, username, generated)
	if err != nil {
		return err
	}
	if created {
		announceBootstrapAdmin(username, generated, cfg.SMTP.Configured())
	}
	return nil
}

// announceBootstrapAdmin 把初始账号一次性打印到日志。
// 密码只在这里出现一次（库里存的是 bcrypt），日志因此是唯一出处。
func announceBootstrapAdmin(username, password string, smtpConfigured bool) {
	log.Printf("==================================================================")
	log.Printf(" 首次部署：已自动创建管理员账号（数据库里原本没有任何用户）")
	log.Printf("   用户名: %s", username)
	log.Printf("   密码:   %s", password)
	log.Printf(" 登录后请到「个人设置 → 修改密码」改掉它。")
	log.Printf(" 想固定账号或密码：设置 YYB_ADMIN_USER / YYB_ADMIN_PASSWORD 后重启容器。")
	if smtpConfigured {
		log.Printf(" 已配置 SMTP：注册页可用，但仍建议先用这个账号登录确认。")
	} else {
		log.Printf(" 未配置 YYB_SMTP_* 时注册页不可用，这个账号就是唯一入口。")
	}
	log.Printf("==================================================================")
}

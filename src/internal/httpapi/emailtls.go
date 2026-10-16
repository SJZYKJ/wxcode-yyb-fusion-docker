package httpapi

// SMTP 的 TLS 连接辅助函数。
//
// 单独成文件是因为它只关心「怎么安全地建一条加密连接」，与验证码逻辑无关；
// 同目录的 emailverify.go 负责验证码与限流。

import (
	"crypto/tls"
	"net"
	"strings"
	"time"
)

// tlsConfigFor 返回对 mailServerName 的 TLS 客户端配置。
//
// 证书校验保持开启（InsecureSkipVerify 恒为 false）：邮箱验证码是账号
// 找回/注册的凭据载体，中间人可拦截就意味着账号可被他人注册。
// 自签证书的内网邮件服务器只能把它加进系统根证书，不能在这里放宽。
func tlsConfigFor(mailServerName string) *tls.Config {
	return &tls.Config{
		ServerName: strings.TrimSpace(mailServerName),
		MinVersion: tls.VersionTLS12,
	}
}

// tlsDial 建立一条立即完成 TLS 握手的连接（SMTPS，465）。
func tlsDial(addr, mailServerName string, timeout time.Duration) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout}
	return tls.DialWithDialer(dialer, "tcp", addr, tlsConfigFor(mailServerName))
}

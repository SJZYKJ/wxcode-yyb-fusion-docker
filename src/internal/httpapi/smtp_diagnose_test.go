package httpapi

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
)

// listenLocal 起一个本地监听，用来让「连通性探测」成功但后续认证由我们伪造失败，
// 从而精确测试「连得上但被拒」这条分支，不依赖外网。
func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

// TestDiagnoseSMTPFailureAuthRejected 覆盖 163/QQ 最常见的「535 认证失败」。
// 这条提示必须点名「授权码」而不是含糊地说密码错误——否则管理员会一直
// 拿登录密码重试。
func TestDiagnoseSMTPFailureAuthRejected(t *testing.T) {
	// 用一个必然连不上的地址，会先命中「连不上」分支；
	// 这里要测的是认证失败分支，所以先用一个「TCP 能连上」的假服务器。
	//
	// 简化处理：直接验证关键词映射逻辑，不依赖真实网络。
	// 通过构造一个监听在本地的 TCP 端口，让连通性探测成功。
	ln := listenLocal(t)
	defer ln.Close()
	cfg := SMTPConfig{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port}

	hint := diagnoseSMTPFailure(cfg, errors.New(`535 "Error: authentication failed"`))
	for _, want := range []string{"535", "授权码", "SMTP"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint missing %q: %s", want, hint)
		}
	}
	if strings.Contains(hint, "无法连接") {
		t.Fatalf("reachable server must not be reported as unreachable: %s", hint)
	}
}

// TestDiagnoseSMTPFailureUnreachable 证明「连不上」与「认证失败」被区分开。
// 这个区分很重要：前者要去查网络，后者才去查密码/授权码。
func TestDiagnoseSMTPFailureUnreachable(t *testing.T) {
	// 保留一个几乎不可能被监听的端口，并指向不可路由的地址。
	cfg := SMTPConfig{Host: "127.0.0.1", Port: 1}
	hint := diagnoseSMTPFailure(cfg, errors.New("dial tcp: connection refused"))
	if !strings.Contains(hint, "无法连接") {
		t.Fatalf("expected unreachable hint, got: %s", hint)
	}
}

// TestDiagnoseSMTPFailureSenderRejected 覆盖 501/553「发件人地址被拒」。
// 很多邮箱要求 From == 登录用户名，这条提示直接指向该检查项。
func TestDiagnoseSMTPFailureSenderRejected(t *testing.T) {
	ln := listenLocal(t)
	defer ln.Close()
	cfg := SMTPConfig{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port}
	hint := diagnoseSMTPFailure(cfg, errors.New("501 mail from address must be same as authorization user"))
	if !strings.Contains(hint, "发件人") {
		t.Fatalf("expected sender hint, got: %s", hint)
	}
}

// TestIsFreemailDomain 保证免费邮箱域名识别正确（决定是否追加「重点查授权码」）。
func TestIsFreemailDomain(t *testing.T) {
	cases := map[string]bool{
		"smtp.163.com":       true,
		"smtp.qq.com":        true,
		"smtp.exmail.qq.com": true,
		"SMTP.163.COM":       true,
		"smtp.example.com":   false,
		"mail.corp.local":    false,
	}
	for host, want := range cases {
		if got := isFreemailDomain(host); got != want {
			t.Fatalf("isFreemailDomain(%q) = %v, want %v", host, got, want)
		}
	}
}

// TestSMTPSettingsTestEmailReturnsHint 保证 PUT 带 test_to 且发信失败时，
// 响应里既有原始 error 也有可执行的中文 hint。
func TestSMTPSettingsTestEmailReturnsHint(t *testing.T) {
	handler, _, cookie := smtpAdminSession(t, Config{
		ResourceRoot: t.TempDir(), AuthDriver: "sqlite", AllowNoAuth: true, SMTP: testSMTP,
	})
	// 至少要带一个可更新字段：handler 对「没有需要更新的字段」返回 400。
	// 这里顺带把端口改成一个必然连不上的值，确保发信失败、走到 hint 分支。
	rec := serveWithHeaders(t, handler, http.MethodPut, "/api/auth/smtp",
		`{"port":2525,"test_to":"nobody@example.com"}`, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "test") {
		t.Fatalf("response missing test result: %s", body)
	}
	// 测试环境里 smtp.example.com 不可达，因此应当拿到「无法连接」类的建议，
	// 而不是一个空 hint。
	if !strings.Contains(body, "hint") {
		t.Fatalf("expected a hint field in the response: %s", body)
	}
}

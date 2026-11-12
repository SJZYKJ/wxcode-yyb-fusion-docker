package proxysource

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseResponseFormats(t *testing.T) {
	tests := []struct {
		name string
		body string
		want Endpoint
	}{
		{"txt", "203.0.113.10:8080\n", Endpoint{Host: "203.0.113.10", Port: 8080}},
		{"txt-auth", "user:pass@203.0.113.11:1080", Endpoint{Host: "203.0.113.11", Port: 1080, Username: "user", Password: "pass"}},
		{"txt-auth-four-columns", "203.0.113.15:1080:user:pass", Endpoint{Host: "203.0.113.15", Port: 1080, Username: "user", Password: "pass"}},
		{"txt-auth-pipes", "203.0.113.16:1080|user|pass", Endpoint{Host: "203.0.113.16", Port: 1080, Username: "user", Password: "pass"}},
		{"ipzan-txt-auth", "203.0.113.17:1080 ipzan-user ipzan-pass\r\n", Endpoint{Host: "203.0.113.17", Port: 1080, Username: "ipzan-user", Password: "ipzan-pass"}},
		{"json", `{"code":0,"data":[{"ip":"203.0.113.12","port":9000}]}`, Endpoint{Host: "203.0.113.12", Port: 9000}},
		{"ipzan-json-auth", `{"data":{"list":[{"ip":"203.0.113.18","port":40006,"expired":1726210338000,"account":"ipzan-account","password":"ipzan-password"}]},"code":0,"message":"","status":200}`, Endpoint{Host: "203.0.113.18", Port: 40006, Username: "ipzan-account", Password: "ipzan-password"}},
		{"json2", `{"success":true,"data":{"proxy_list":[{"host":"203.0.113.13","proxy_port":"9001","username":"u","password":"p"}]}}`, Endpoint{Host: "203.0.113.13", Port: 9001, Username: "u", Password: "p"}},
		{"juliang-json2-auth", `{"code":200,"msg":"成功","data":{"proxy_list":[{"city":"潍坊","http_pass":"juliang-pass","http_user":"juliang-user","ip":"203.0.113.19","port":"30030","province":"山东"}]}}`, Endpoint{Host: "203.0.113.19", Port: 30030, Username: "juliang-user", Password: "juliang-pass"}},
		{"nested-string", `{"result":{"proxy":"203.0.113.14:9002"}}`, Endpoint{Host: "203.0.113.14", Port: 9002}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseResponse([]byte(test.body))
			if err != nil {
				t.Fatalf("ParseResponse() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("ParseResponse() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseResponseReportsProviderMessage(t *testing.T) {
	_, err := ParseResponse([]byte(`{"code":-1,"message":"当前IP不在白名单"}`))
	if err == nil || !strings.Contains(err.Error(), "当前IP不在白名单") {
		t.Fatalf("ParseResponse() error = %v", err)
	}
}

func TestResolveProxyAPIAndNormalizeStatic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"ip":"203.0.113.20","port":8123}}`))
	}))
	defer server.Close()

	got, err := Resolve(context.Background(), server.Client(), Spec{Mode: "api", ProxyType: "socks5", APIURL: server.URL})
	if err != nil || got != "socks5://203.0.113.20:8123" {
		t.Fatalf("Resolve() = %q, %v", got, err)
	}
	normalized, err := NormalizeSpec(Spec{Mode: "static", ProxyType: "http", StaticProxy: "user:pass@203.0.113.21:8080"})
	if err != nil || !strings.HasPrefix(normalized.StaticProxy, "http-connect://user:pass@") {
		t.Fatalf("NormalizeSpec() = %#v, %v", normalized, err)
	}
}

func TestNormalizeSpecRejectsInvalidConfiguration(t *testing.T) {
	tests := []Spec{
		{Mode: "unknown"},
		{Mode: "static", ProxyType: "https", StaticProxy: "127.0.0.1:8080"},
		{Mode: "static", StaticProxy: "missing-port"},
		{Mode: "api", APIURL: "file:///tmp/proxy.txt"},
	}
	for _, spec := range tests {
		if _, err := NormalizeSpec(spec); err == nil {
			t.Fatalf("NormalizeSpec(%#v) unexpectedly succeeded", spec)
		}
	}
}

package proxysource

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const responseLimit = 1 << 20

type Spec struct {
	Mode        string `json:"mode"`
	ProxyType   string `json:"proxy_type"`
	StaticProxy string `json:"static_proxy"`
	APIURL      string `json:"api_url"`
}

type Endpoint struct {
	Host     string
	Port     int
	Username string
	Password string
}

func NormalizeSpec(spec Spec) (Spec, error) {
	spec.Mode = strings.ToLower(strings.TrimSpace(spec.Mode))
	spec.ProxyType = strings.ToLower(strings.TrimSpace(spec.ProxyType))
	spec.StaticProxy = strings.TrimSpace(spec.StaticProxy)
	spec.APIURL = strings.TrimSpace(spec.APIURL)
	if spec.Mode == "" {
		spec.Mode = "direct"
	}
	if spec.ProxyType == "" {
		spec.ProxyType = "http"
	}
	if spec.ProxyType == "http-connect" {
		spec.ProxyType = "http"
	}
	if spec.Mode != "direct" && spec.Mode != "static" && spec.Mode != "api" {
		return Spec{}, fmt.Errorf("代理模式必须为 direct、static 或 api")
	}
	if spec.ProxyType != "http" && spec.ProxyType != "socks5" {
		return Spec{}, fmt.Errorf("代理类型必须为 http 或 socks5")
	}
	switch spec.Mode {
	case "direct":
		spec.StaticProxy, spec.APIURL = "", ""
	case "static":
		endpoint, err := ParseEndpoint(spec.StaticProxy)
		if err != nil {
			return Spec{}, fmt.Errorf("静态代理无效: %w", err)
		}
		spec.StaticProxy = endpoint.ProxyURL(spec.ProxyType)
		spec.APIURL = ""
	case "api":
		u, err := url.Parse(spec.APIURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return Spec{}, fmt.Errorf("代理 API 必须是有效的 http/https URL")
		}
		spec.StaticProxy = ""
	}
	return spec, nil
}

func Resolve(ctx context.Context, client *http.Client, spec Spec) (string, error) {
	normalized, err := NormalizeSpec(spec)
	if err != nil {
		return "", err
	}
	switch normalized.Mode {
	case "direct":
		return "", nil
	case "static":
		return normalized.StaticProxy, nil
	}
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, normalized.APIURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求代理 API 失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit+1))
	if err != nil {
		return "", fmt.Errorf("读取代理 API 响应失败: %w", err)
	}
	if len(body) > responseLimit {
		return "", fmt.Errorf("代理 API 响应超过 1 MB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("代理 API 返回 HTTP %d", resp.StatusCode)
	}
	endpoint, err := ParseResponse(body)
	if err != nil {
		return "", err
	}
	return endpoint.ProxyURL(normalized.ProxyType), nil
}

func ParseResponse(body []byte) (Endpoint, error) {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return Endpoint{}, fmt.Errorf("代理 API 返回空响应")
	}
	var value any
	if json.Unmarshal(body, &value) == nil {
		if endpoint, ok := endpointFromJSON(value); ok {
			return endpoint, nil
		}
		if message := responseMessage(value); message != "" {
			return Endpoint{}, fmt.Errorf("代理 API 返回错误: %s", message)
		}
	}
	for _, line := range strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';'
	}) {
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			if endpoint, err := ParseEndpoint(fields[0]); err == nil {
				endpoint.Username = fields[1]
				endpoint.Password = fields[2]
				return endpoint, nil
			}
		}
		if endpoint, err := ParseEndpoint(line); err == nil {
			return endpoint, nil
		}
	}
	return Endpoint{}, fmt.Errorf("无法从代理 API 响应解析 IP 和端口，支持 txt、json、json2")
}

func responseMessage(value any) string {
	values, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"message", "msg", "error"} {
		if message, exists := stringValue(values, key); exists && message != "" {
			return message
		}
	}
	return ""
}

func ParseEndpoint(raw string) (Endpoint, error) {
	raw = strings.TrimSpace(strings.Trim(raw, `"'`))
	if raw == "" {
		return Endpoint{}, fmt.Errorf("代理地址为空")
	}
	if endpoint, ok := parseHostPortCredentials(raw); ok {
		return endpoint, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.Port() == "" {
		return Endpoint{}, fmt.Errorf("代理地址必须包含 host:port")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return Endpoint{}, fmt.Errorf("代理端口无效")
	}
	endpoint := Endpoint{Host: u.Hostname(), Port: port}
	if u.User != nil {
		endpoint.Username = u.User.Username()
		endpoint.Password, _ = u.User.Password()
	}
	return endpoint, nil
}

func parseHostPortCredentials(raw string) (Endpoint, bool) {
	parts := strings.Split(raw, ":")
	if len(parts) == 4 && !strings.Contains(parts[0], "://") && !strings.Contains(parts[0], "[") {
		if endpoint, ok := endpointFromParts(parts[0], parts[1], parts[2], parts[3]); ok {
			return endpoint, true
		}
	}
	parts = strings.Split(raw, "|")
	if len(parts) == 3 {
		host, port, err := net.SplitHostPort(parts[0])
		if err == nil {
			if endpoint, ok := endpointFromParts(host, port, parts[1], parts[2]); ok {
				return endpoint, true
			}
		}
	}
	return Endpoint{}, false
}

func endpointFromParts(host, rawPort, username, password string) (Endpoint, bool) {
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 || strings.TrimSpace(host) == "" || username == "" {
		return Endpoint{}, false
	}
	return Endpoint{Host: strings.TrimSpace(host), Port: port, Username: username, Password: password}, true
}

func (e Endpoint) ProxyURL(proxyType string) string {
	scheme := "http-connect"
	if strings.EqualFold(proxyType, "socks5") {
		scheme = "socks5"
	}
	u := &url.URL{Scheme: scheme, Host: net.JoinHostPort(e.Host, strconv.Itoa(e.Port))}
	if e.Username != "" {
		u.User = url.UserPassword(e.Username, e.Password)
	}
	return u.String()
}

func Mask(proxyURL string) string {
	u, err := url.Parse(proxyURL)
	if err != nil || u.Host == "" {
		return ""
	}
	u.User = nil
	return u.String()
}

func endpointFromJSON(value any) (Endpoint, bool) {
	switch current := value.(type) {
	case string:
		endpoint, err := ParseEndpoint(current)
		return endpoint, err == nil
	case []any:
		for _, item := range current {
			if endpoint, ok := endpointFromJSON(item); ok {
				return endpoint, true
			}
		}
	case map[string]any:
		if endpoint, ok := endpointFromMap(current); ok {
			return endpoint, true
		}
		for _, key := range []string{"data", "result", "list", "proxy", "proxies", "proxy_list", "rows", "items", "obj"} {
			if nested, exists := lookupFold(current, key); exists {
				if endpoint, ok := endpointFromJSON(nested); ok {
					return endpoint, true
				}
			}
		}
		for _, nested := range current {
			if endpoint, ok := endpointFromJSON(nested); ok {
				return endpoint, true
			}
		}
	}
	return Endpoint{}, false
}

func endpointFromMap(values map[string]any) (Endpoint, bool) {
	for _, key := range []string{"proxy", "server", "address"} {
		if raw, ok := stringValue(values, key); ok {
			if endpoint, err := ParseEndpoint(raw); err == nil {
				applyCredentials(&endpoint, values)
				return endpoint, true
			}
		}
	}
	host := firstString(values, "ip", "host", "server_ip", "proxy_ip")
	port := firstInt(values, "port", "proxy_port")
	if host == "" || port < 1 || port > 65535 {
		return Endpoint{}, false
	}
	endpoint := Endpoint{Host: strings.Trim(host, "[]"), Port: port}
	applyCredentials(&endpoint, values)
	return endpoint, true
}

func applyCredentials(endpoint *Endpoint, values map[string]any) {
	endpoint.Username = firstString(values, "user", "username", "account", "proxy_user", "http_user")
	endpoint.Password = firstString(values, "pass", "password", "pwd", "proxy_pass", "http_pass")
}

func lookupFold(values map[string]any, wanted string) (any, bool) {
	for key, value := range values {
		if strings.EqualFold(key, wanted) {
			return value, true
		}
	}
	return nil, false
}

func stringValue(values map[string]any, key string) (string, bool) {
	value, ok := lookupFold(values, key)
	if !ok {
		return "", false
	}
	switch current := value.(type) {
	case string:
		return strings.TrimSpace(current), true
	case json.Number:
		return current.String(), true
	case float64:
		return strconv.FormatInt(int64(current), 10), true
	}
	return "", false
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := stringValue(values, key); ok && value != "" {
			return value
		}
	}
	return ""
}

func firstInt(values map[string]any, keys ...string) int {
	for _, key := range keys {
		if value, ok := stringValue(values, key); ok {
			if result, err := strconv.Atoi(value); err == nil {
				return result
			}
		}
	}
	return 0
}

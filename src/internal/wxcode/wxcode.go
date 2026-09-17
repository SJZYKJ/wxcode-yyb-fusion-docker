// Package wxcode adapts the on-device wxcode Xposed service (default
// http://127.0.0.1:8088) as an alternative code source for YYB Go. The
// wxcode service hooks the local WeChat app and answers
//
//	GET /login?appId=<appid>
//
// with a JSON payload in the shape documented below.
package wxcode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// LoginResult mirrors the successful wxcode /login response:
//
//	{"err":0,"msg":"success","appId":"...","status":"...","code":"...",
//	 "codeType":"hex|base64|...","codeLength":123}
type LoginResult struct {
	Err        int    `json:"err"`
	Msg        string `json:"msg"`
	AppID      string `json:"appId"`
	Status     string `json:"status"`
	Code       string `json:"code"`
	CodeType   string `json:"codeType"`
	CodeLength int    `json:"codeLength"`
}

// SourceStatus is a summary of one configured wxcode endpoint.
type SourceStatus struct {
	URL     string `json:"url"`
	Online  bool   `json:"online"`
	Detail  string `json:"detail,omitempty"`
	Checked string `json:"checked_at"`
}

// Client fans out to one or more wxcode HTTP services. Endpoints are tried
// in order for code requests, which mirrors the multi-device YYB_SERVER
// behavior used by the automation scripts.
type Client struct {
	baseURLs []string
	timeout  time.Duration
}

// NewClient normalizes the supplied endpoints (scheme optional, default
// http) and returns a client that talks to them.
func NewClient(endpoints []string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 40 * time.Second
	}
	c := &Client{timeout: timeout}
	for _, raw := range endpoints {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			c.baseURLs = append(c.baseURLs, strings.TrimRight(raw, "/"))
		}
	}
	return c
}

// Enabled reports whether at least one endpoint is configured.
func (c *Client) Enabled() bool {
	return c != nil && len(c.baseURLs) > 0
}

// Endpoints returns the normalized endpoint list.
func (c *Client) Endpoints() []string {
	if c == nil {
		return nil
	}
	out := make([]string, len(c.baseURLs))
	copy(out, c.baseURLs)
	return out
}

// GetCode asks every configured wxcode endpoint in order for a login code
// for appID. The first success wins; failed endpoints are collected into
// the returned error.
func (c *Client) GetCode(ctx context.Context, appID string) (LoginResult, error) {
	if !c.Enabled() {
		return LoginResult{}, fmt.Errorf("no wxcode endpoints configured")
	}
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return LoginResult{}, fmt.Errorf("appId is required")
	}

	var failures []string
	for _, base := range c.baseURLs {
		result, err := c.getCodeFrom(ctx, base, appID)
		if err == nil {
			return result, nil
		}
		failures = append(failures, fmt.Sprintf("%s: %v", base, err))
	}
	return LoginResult{}, fmt.Errorf("all wxcode endpoints failed: %s", strings.Join(failures, "; "))
}

func (c *Client) getCodeFrom(ctx context.Context, base, appID string) (LoginResult, error) {
	reqURL := base + "/login?appId=" + url.QueryEscape(appID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return LoginResult{}, err
	}
	client := &http.Client{Timeout: c.timeout}
	resp, err := client.Do(req)
	if err != nil {
		return LoginResult{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return LoginResult{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return LoginResult{}, fmt.Errorf("http %d: %s", resp.StatusCode, summarize(body))
	}
	var result LoginResult
	if err := json.Unmarshal(body, &result); err != nil {
		return LoginResult{}, fmt.Errorf("invalid JSON: %v", err)
	}
	if result.Err != 0 {
		return LoginResult{}, fmt.Errorf("wxcode err=%d msg=%s", result.Err, result.Msg)
	}
	if result.Code == "" {
		return LoginResult{}, fmt.Errorf("wxcode returned an empty code")
	}
	return result, nil
}

// Status probes every configured endpoint with a short timeout and returns
// per-endpoint health suitable for the console status widget.
func (c *Client) Status(ctx context.Context) []SourceStatus {
	if c == nil {
		return nil
	}
	out := make([]SourceStatus, 0, len(c.baseURLs))
	for _, base := range c.baseURLs {
		st := SourceStatus{URL: base, Checked: time.Now().Format(time.RFC3339)}
		reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/whoami", nil)
		if err == nil {
			client := &http.Client{Timeout: 5 * time.Second}
			resp, respErr := client.Do(req)
			if respErr == nil {
				body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
				resp.Body.Close()
				if readErr == nil && resp.StatusCode == http.StatusOK {
					st.Online = true
					st.Detail = summarize(body)
				} else {
					st.Detail = fmt.Sprintf("http %d", resp.StatusCode)
				}
			} else {
				st.Detail = respErr.Error()
			}
		} else {
			st.Detail = err.Error()
		}
		cancel()
		out = append(out, st)
	}
	return out
}

func summarize(data []byte) string {
	s := strings.TrimSpace(string(data))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

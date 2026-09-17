package oauth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

const authorizeEndpoint = "https://open.weixin.qq.com/connect/oauth2/authorize"

// Request contains the public-account OAuth parameters accepted by YYB Go.
// The account reference is handled by the HTTP layer; this package only builds
// and validates the WeChat authorization URL.
type Request struct {
	AppID          string
	RedirectURI    string
	Scope          string
	State          string
	ComponentAppID string
}

type Result struct {
	URL     string
	FullURL string
	Scope   string
	State   string
}

func Build(req Request) (Result, error) {
	appID := strings.TrimSpace(req.AppID)
	if len(appID) != 18 || !strings.HasPrefix(appID, "wx") {
		return Result{}, fmt.Errorf("appid must be an 18-character public-account AppID")
	}

	redirectURI, err := validateRedirectURI(req.RedirectURI)
	if err != nil {
		return Result{}, err
	}

	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		scope = "snsapi_base"
	}
	if scope != "snsapi_base" && scope != "snsapi_userinfo" {
		return Result{}, fmt.Errorf("scope must be snsapi_base or snsapi_userinfo")
	}

	state := strings.TrimSpace(req.State)
	if state == "" {
		state, err = randomState()
		if err != nil {
			return Result{}, fmt.Errorf("generate state: %w", err)
		}
	}
	if len(state) > 128 || strings.ContainsAny(state, "\r\n") {
		return Result{}, fmt.Errorf("state must be at most 128 characters")
	}

	componentAppID := strings.TrimSpace(req.ComponentAppID)
	if componentAppID != "" && (len(componentAppID) != 18 || !strings.HasPrefix(componentAppID, "wx")) {
		return Result{}, fmt.Errorf("component_appid must be an 18-character AppID")
	}

	values := url.Values{}
	values.Set("appid", appID)
	values.Set("redirect_uri", redirectURI)
	values.Set("response_type", "code")
	values.Set("scope", scope)
	values.Set("state", state)
	if componentAppID != "" {
		values.Set("component_appid", componentAppID)
	}

	base := authorizeEndpoint + "?" + values.Encode()
	return Result{URL: base, FullURL: base + "#wechat_redirect", Scope: scope, State: state}, nil
}

func validateRedirectURI(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n") {
		return "", fmt.Errorf("redirect_uri is required and must be at most 2048 characters")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("redirect_uri must be an absolute URL without credentials or fragments")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("redirect_uri must use http or https")
	}
	return u.String(), nil
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

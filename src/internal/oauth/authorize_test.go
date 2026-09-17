package oauth

import (
	"net/url"
	"strings"
	"testing"
)

func TestBuild(t *testing.T) {
	result, err := Build(Request{
		AppID:       "wx1234567890abcdef",
		RedirectURI: "https://example.com/callback?source=yyb",
		Scope:       "snsapi_userinfo",
		State:       "state-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(result.FullURL, "#wechat_redirect") {
		t.Fatalf("full URL missing WeChat fragment: %s", result.FullURL)
	}
	parsed, err := url.Parse(result.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if q.Get("appid") != "wx1234567890abcdef" || q.Get("scope") != "snsapi_userinfo" || q.Get("state") != "state-1" {
		t.Fatalf("unexpected query: %#v", q)
	}
	if q.Get("redirect_uri") != "https://example.com/callback?source=yyb" {
		t.Fatalf("redirect URI was not preserved: %s", q.Get("redirect_uri"))
	}
}

func TestBuildRejectsUnsafeOrInvalidInput(t *testing.T) {
	tests := []Request{
		{AppID: "wx123", RedirectURI: "https://example.com/callback"},
		{AppID: "wx1234567890abcdef", RedirectURI: "//example.com/callback"},
		{AppID: "wx1234567890abcdef", RedirectURI: "https://example.com/callback#code"},
		{AppID: "wx1234567890abcdef", RedirectURI: "https://example.com/callback", Scope: "invalid"},
	}
	for i, test := range tests {
		if _, err := Build(test); err == nil {
			t.Errorf("case %d unexpectedly succeeded", i)
		}
	}
}

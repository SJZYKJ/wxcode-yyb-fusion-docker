package wxcode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientGetCodeSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("appId") != "wx-test" {
			t.Errorf("unexpected appId: %s", r.URL.Query().Get("appId"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"err": 0, "msg": "success", "appId": "wx-test", "status": "ok",
			"code": "0abc", "codeType": "hex", "codeLength": 4,
		})
	}))
	defer srv.Close()

	client := NewClient([]string{srv.URL}, 5*time.Second)
	result, err := client.GetCode(context.Background(), "wx-test")
	if err != nil {
		t.Fatalf("GetCode: %v", err)
	}
	if result.Code != "0abc" || result.CodeType != "hex" || result.CodeLength != 4 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestClientGetCodeFailover(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"err":-500,"msg":"boom"}`))
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"err":0,"msg":"success","appId":"wx-test","code":"0good","codeType":"hex","codeLength":5}`))
	}))
	defer good.Close()

	client := NewClient([]string{bad.URL, good.URL}, 5*time.Second)
	result, err := client.GetCode(context.Background(), "wx-test")
	if err != nil {
		t.Fatalf("GetCode should fail over to the healthy endpoint: %v", err)
	}
	if result.Code != "0good" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestClientGetCodeAllFail(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"err":-500,"msg":"boom"}`))
	}))
	defer bad.Close()

	client := NewClient([]string{bad.URL}, 5*time.Second)
	if _, err := client.GetCode(context.Background(), "wx-test"); err == nil {
		t.Fatal("expected error when all endpoints fail")
	}
}

func TestClientStatus(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"package":"com.tencent.mm","user":0}`))
	}))
	defer ok.Close()

	client := NewClient([]string{ok.URL}, 5*time.Second)
	statuses := client.Status(context.Background())
	if len(statuses) != 1 || !statuses[0].Online {
		t.Fatalf("unexpected status: %+v", statuses)
	}
}

func TestNewClientNormalizesURLs(t *testing.T) {
	client := NewClient([]string{"127.0.0.1:8088", "http://example.com:9000/", "", "  "}, 5*time.Second)
	if len(client.Endpoints()) != 2 {
		t.Fatalf("expected 2 endpoints, got %v", client.Endpoints())
	}
	if client.Endpoints()[0] != "http://127.0.0.1:8088" {
		t.Fatalf("expected scheme normalization, got %q", client.Endpoints()[0])
	}
	if client.Endpoints()[1] != "http://example.com:9000" {
		t.Fatalf("expected trailing slash removal, got %q", client.Endpoints()[1])
	}
}

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 青龙新版本对「新建/空目录」返回 type:"directory" 且 children 为空，甚至不带
// children 字段。网关必须用 ?path= 主动单层拉取该目录，否则里面的脚本会整体丢失。
func TestQingLongDriverRecursesIntoEmptyChildDirs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/open/auth/token" {
			if r.Header.Get("Authorization") != "Bearer token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		switch {
		case r.URL.Path == "/open/auth/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"token": "token", "expiration": 3600}})
		case r.URL.Path == "/open/scripts/files" && r.URL.Query().Get("path") == "":
			// 根层：目录「应用宝版」没有 children（青龙新版的坑）
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": []qingLongScriptNode{
				{Title: "应用宝版", Key: "应用宝版", Type: "directory"},
				{Title: "绿鼻子.js", Key: "绿鼻子.js", Type: "file"},
			}})
		case r.URL.Path == "/open/scripts/files" && r.URL.Query().Get("path") == "应用宝版":
			// 单层拉取：新目录里的脚本在这里才出现
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": []qingLongScriptNode{
				{Title: "测试脚本.py", Key: "应用宝版/测试脚本.py", Type: "file", Parent: "应用宝版"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	driver := newQingLongDriver(server.URL, "id", "secret", 5*time.Second)
	scripts, err := driver.ListScripts(context.Background())
	if err != nil {
		t.Fatalf("ListScripts() error = %v", err)
	}
	found := false
	for _, s := range scripts {
		if s.Path == "应用宝版/测试脚本.py" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListScripts() 漏掉了新建目录里的脚本，实际返回：%+v", scripts)
	}
	if len(scripts) != 2 {
		t.Fatalf("期望 2 个脚本，实际 %d：%+v", len(scripts), scripts)
	}
}

func TestQingLongDriverReadsLargeLogIndex(t *testing.T) {
	children := make([]qingLongLogEntry, 0, 30000)
	for i := 0; i < cap(children); i++ {
		children = append(children, qingLongLogEntry{Title: strings.Repeat("x", 90), Key: "logs/file.log", Type: "file"})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open/auth/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"token": "token", "expiration": 3600}})
		case "/open/logs":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": []qingLongLogEntry{{Title: "large", Key: "large", Type: "directory", Children: children}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	driver := newQingLongDriver(server.URL, "id", "secret", 5*time.Second)
	logs, err := driver.ListLogs(context.Background())
	if err != nil {
		t.Fatalf("ListLogs() error = %v", err)
	}
	if len(logs) != 1 || len(logs[0].Children) != len(children) {
		t.Fatalf("ListLogs() returned %d roots and %d children", len(logs), len(logs[0].Children))
	}
}

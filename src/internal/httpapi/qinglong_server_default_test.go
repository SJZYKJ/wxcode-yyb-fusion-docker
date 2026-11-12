package httpapi

import (
	"path/filepath"
	"testing"
)

// 面板回连地址的默认值必须跟着实际监听端口走：容器里网关监听 8088 时，
// 写死 8000 会让青龙脚本连到一个没人监听的端口。
func TestDefaultQingLongServerFollowsListenPort(t *testing.T) {
	cases := []struct {
		name string
		port int
		want string
	}{
		{name: "docker wrapper port", port: 8088, want: "yyb-go:8088"},
		{name: "source compose port", port: 8000, want: "yyb-go:8000"},
		{name: "unset falls back", port: 0, want: "yyb-go:8000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			app, err := NewApp(Config{
				ResourceRoot: filepath.Join(dir, "resource"),
				DBFilename:   "test.db",
				Port:         tc.port,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			defer app.Close()
			if got := app.cfg.QingLongServer; got != tc.want {
				t.Fatalf("QingLongServer = %q, want %q", got, tc.want)
			}
		})
	}
}

package main

import "testing"

// 安全相关的布尔开关一旦解析不出来就会静默退回「不安全/不生效」，
// 所以这里把可接受的字面量逐个钉住。
func TestEnvBoolAcceptedLiterals(t *testing.T) {
	for _, raw := range []string{"1", "true", "TRUE", "True", "yes", "YES", "on", " on "} {
		t.Setenv("YYB_TEST_BOOL", raw)
		if !envBool("YYB_TEST_BOOL") {
			t.Errorf("envBool(%q) = false, want true", raw)
		}
	}
	for _, raw := range []string{"", "0", "false", "no", "off", "maybe", "2"} {
		t.Setenv("YYB_TEST_BOOL", raw)
		if envBool("YYB_TEST_BOOL") {
			t.Errorf("envBool(%q) = true, want false", raw)
		}
	}
}

package main

import (
	"net"
	"reflect"
	"testing"
)

func TestParseDNSServers(t *testing.T) {
	servers, err := parseDNSServers("223.5.5.5:53, 119.29.29.29;2001:4860:4860::8888")
	if err != nil {
		t.Fatalf("parseDNSServers() error = %v", err)
	}
	want := []string{"223.5.5.5:53", "119.29.29.29:53", "[2001:4860:4860::8888]:53"}
	if !reflect.DeepEqual(servers, want) {
		t.Fatalf("parseDNSServers() = %#v, want %#v", servers, want)
	}
	if _, err := parseDNSServers("dns.example.com"); err == nil {
		t.Fatal("parseDNSServers() accepted a hostname")
	}
}

func TestConfigureDNS(t *testing.T) {
	original := net.DefaultResolver
	t.Cleanup(func() { net.DefaultResolver = original })
	servers, err := configureDNS("223.5.5.5:53,119.29.29.29:53")
	if err != nil {
		t.Fatalf("configureDNS() error = %v", err)
	}
	if len(servers) != 2 || !net.DefaultResolver.PreferGo || net.DefaultResolver.Dial == nil {
		t.Fatalf("configureDNS() did not install resolver: servers=%v resolver=%#v", servers, net.DefaultResolver)
	}
}

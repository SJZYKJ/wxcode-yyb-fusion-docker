package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func configureDNS(raw string) ([]string, error) {
	servers, err := parseDNSServers(raw)
	if err != nil || len(servers) == 0 {
		return servers, err
	}
	var next atomic.Uint32
	net.DefaultResolver = &net.Resolver{
		PreferGo:     true,
		StrictErrors: false,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			server := servers[int(next.Add(1)-1)%len(servers)]
			if network != "tcp" && network != "tcp4" && network != "tcp6" {
				network = "udp"
			}
			dialer := net.Dialer{Timeout: 4 * time.Second}
			return dialer.DialContext(ctx, network, server)
		},
	}
	return servers, nil
}

func parseDNSServers(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == ' ' })
	servers := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		host, port, err := net.SplitHostPort(value)
		if err != nil {
			if net.ParseIP(value) == nil {
				return nil, fmt.Errorf("invalid DNS server %q; use IP or IP:port", value)
			}
			host, port = value, "53"
		}
		if net.ParseIP(host) == nil {
			return nil, fmt.Errorf("invalid DNS server IP %q", host)
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return nil, fmt.Errorf("invalid DNS server port %q", port)
		}
		servers = append(servers, net.JoinHostPort(host, port))
	}
	return servers, nil
}

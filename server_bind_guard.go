//go:build server

package main

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// enforceServerBindAddress keeps the raw Wails server mode local-only. The
// authenticated Docker gateway is the supported remote deployment boundary.
func enforceServerBindAddress() error {
	host := strings.TrimSpace(os.Getenv("WAILS_SERVER_HOST"))
	if host == "" {
		return os.Setenv("WAILS_SERVER_HOST", "127.0.0.1")
	}
	if !isLoopbackServerHost(host) {
		return fmt.Errorf("server mode refuses non-loopback WAILS_SERVER_HOST %q; use the authenticated Docker gateway for remote access", host)
	}
	return nil
}

func isLoopbackServerHost(host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

//go:build server

package main

import (
	"os"
	"testing"
)

func TestEnforceServerBindAddressDefaultsToLoopback(t *testing.T) {
	t.Setenv("WAILS_SERVER_HOST", "")
	if err := enforceServerBindAddress(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("WAILS_SERVER_HOST"); got != "127.0.0.1" {
		t.Fatalf("WAILS_SERVER_HOST = %q, want 127.0.0.1", got)
	}
}

func TestEnforceServerBindAddressRejectsRemoteHosts(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "192.0.2.10", "example.test", "[2001:db8::1]"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("WAILS_SERVER_HOST", host)
			if err := enforceServerBindAddress(); err == nil {
				t.Fatalf("enforceServerBindAddress(%q) succeeded; want rejection", host)
			}
		})
	}
}

func TestEnforceServerBindAddressAllowsLoopbackHosts(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "[::1]"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("WAILS_SERVER_HOST", host)
			if err := enforceServerBindAddress(); err != nil {
				t.Fatalf("enforceServerBindAddress(%q) = %v", host, err)
			}
		})
	}
}

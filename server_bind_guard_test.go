//go:build server

package main

import (
	"os"
	"testing"
)

func TestServerBindDefaultsToLoopback(t *testing.T) {
	t.Setenv("WAILS_SERVER_HOST", "")
	if err := enforceServerBindAddress(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("WAILS_SERVER_HOST"); got != "127.0.0.1" {
		t.Fatalf("default WAILS_SERVER_HOST = %q, want 127.0.0.1", got)
	}
}

func TestServerBindRejectsPublicHost(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "192.0.2.10", "::"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("WAILS_SERVER_HOST", host)
			if err := enforceServerBindAddress(); err == nil {
				t.Fatalf("enforceServerBindAddress(%q) succeeded; want rejection", host)
			}
		})
	}
}

package services

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLSPRealServerCompatibilityMatrix(t *testing.T) {
	if os.Getenv("CI") == "" && os.Getenv("KOYORI_IDE_LSP_INTEGRATION") != "1" {
		t.Skip("set KOYORI_IDE_LSP_INTEGRATION=1 to run real LSP compatibility tests")
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/lspcompat\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"private":true}`), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	for _, server := range []struct {
		name     string
		language string
		kind     string
	}{
		{name: "gopls", language: "go", kind: "gopls"},
		{name: "typescript-language-server", language: "typescript", kind: "typescript-language-server"},
		{name: "vtsls", language: "typescript", kind: "vtsls"},
	} {
		t.Run(server.name, func(t *testing.T) {
			executable, err := exec.LookPath(server.name)
			if err != nil {
				t.Skipf("%s is not installed", server.name)
			}
			process, stdin, stdout, err := startServerProcess(server.language, executable, server.kind, root)
			if err != nil {
				t.Fatalf("start %s: %v", server.name, err)
			}
			svc := NewLSPService(root)
			client := newJSONRPCClientWithHandler(stdout, stdin, svc.handleServerRequest)
			srv := &lspServer{process: process, client: client}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), lspProcessStopTimeout)
				defer cancel()
				if err := stopLSPServerProcess(ctx, srv, errLSPServerStopping); err != nil {
					t.Errorf("stop %s: %v", server.name, err)
				}
			})

			started := time.Now()
			if err := svc.initializeLocked(srv, server.language, root, nil); err != nil {
				t.Fatalf("initialize %s: %v", server.name, err)
			}
			if elapsed := time.Since(started); elapsed >= lspRequestTimeout {
				t.Fatalf("initialize %s took %v", server.name, elapsed)
			}
		})
	}
}

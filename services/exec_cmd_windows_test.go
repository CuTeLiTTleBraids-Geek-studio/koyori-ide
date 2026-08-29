//go:build windows

package services

import (
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	cmdProbeMode   = "GUGACODE_CMD_PROBE"
	cmdProbeOutput = "GUGACODE_CMD_PROBE_OUTPUT"
)

// TestCommandCmdShimRoundTrip is an end-to-end cmd.exe test. The .cmd file
// receives the arguments and forwards them to this test binary, which writes
// their byte values one per line so no display encoding is involved.
func TestCommandCmdShimRoundTrip(t *testing.T) {
	if os.Getenv(cmdProbeMode) == "1" {
		t.Skip("probe helper")
	}

	dir := t.TempDir()
	probeDir := filepath.Join(dir, "probe dir")
	if err := os.MkdirAll(probeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(probeDir, "probe.cmd")
	contents := "@echo off\r\nsetlocal DisableDelayedExpansion\r\n" +
		"\"" + os.Args[0] + "\" -test.run=TestCmdProbeHelper -- %*\r\n"
	if err := os.WriteFile(probe, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	baseArgs := []string{
		"a & calc",
		"x | whoami",
		"y > out",
		"z < in",
		"100%",
		"%PATH%",
		"say !hi!",
		"p ^ q",
		`embedded "quote"`,
		"",
		" ",
		"こんにちは",
	}

	for _, tc := range []struct {
		name  string
		start func(...string) *exec.Cmd
	}{
		{name: "command", start: func(args ...string) *exec.Cmd { return command(probe, args...) }},
		{name: "commandContext", start: func(args ...string) *exec.Cmd {
			return commandContext(context.Background(), probe, args...)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := filepath.Join(dir, tc.name+"-argv.hex")
			marker := filepath.Join(dir, tc.name+"-injected.marker")
			payload := "safe & echo injected>\"" + marker + "\""
			want := append(append([]string(nil), baseArgs...), payload)

			cmd := tc.start(want...)
			cmd.Env = append(os.Environ(), cmdProbeMode+"=1", cmdProbeOutput+"="+output)
			if err := cmd.Run(); err != nil {
				t.Fatalf("%s(%q) failed: %v", tc.name, probe, err)
			}

			raw, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSuffix(string(raw), "\r\n"), "\r\n")
			if len(lines) != len(want) {
				t.Fatalf("probe received %d args, want %d: %q", len(lines), len(want), raw)
			}
			for i, line := range lines {
				got, err := hex.DecodeString(line)
				if err != nil {
					t.Fatalf("probe output line %d is not hex: %q", i, line)
				}
				if string(got) != want[i] {
					t.Errorf("argv[%d] = %q, want %q", i, string(got), want[i])
				}
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("injection marker exists: %q", marker)
			}
		})
	}
}

func TestCmdProbeHelper(t *testing.T) {
	if os.Getenv(cmdProbeMode) != "1" {
		return
	}
	output := os.Getenv(cmdProbeOutput)
	if output == "" {
		t.Fatal("missing probe output path")
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		t.Fatal("missing probe argument separator")
	}
	var lines strings.Builder
	for _, arg := range os.Args[separator+1:] {
		lines.WriteString(hex.EncodeToString([]byte(arg)))
		lines.WriteString("\r\n")
	}
	if err := os.WriteFile(output, []byte(lines.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

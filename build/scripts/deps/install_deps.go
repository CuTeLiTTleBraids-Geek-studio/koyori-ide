// install_deps.go — G-OFF-02: Go/TS/JS toolchain detection script.
//
// This standalone script checks that the local toolchain required for the
// Koyori IDE's offline features is installed and reachable on PATH. It is
// shipped alongside the Wails single-binary distribution so that a first run
// can detect go / node / gopls (and friends) and guide the user through
// installation when a tool is missing.
//
// All checks are local (exec.LookPath) — the script itself works fully
// offline, which matches the G-OFF-01 guarantee.
//
// Usage:
//
//	go run build/scripts/deps/install_deps.go
//
// Exit codes:
//
//	0 — all critical tools are installed
//	1 — one or more critical tools are missing (recommended tools only warn)
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// tool describes a single dependency and how to install it.
type tool struct {
	// name is the executable looked up on PATH.
	name string
	// label is the human-friendly name printed in the report.
	label string
	// critical is true when the IDE's core offline features depend on it.
	// Missing critical tools cause a non-zero exit. Non-critical (recommended)
	// tools only print a warning.
	critical bool
	// versionArgs is run against the resolved binary to capture a version
	// string. May be empty, in which case only the path is reported.
	versionArgs []string
	// installHint is printed when the tool is missing.
	installHint string
}

// requiredTools is the catalog of dependencies checked by this script.
//
// Critical tools (go, node, git) are needed for the IDE to deliver its core
// offline promise: editing, LSP, completion, local Git, build, test, search.
// Recommended tools (gopls, tsserver/typescript, golangci-lint, eslint) power
// higher-fidelity language features and linting — the IDE degrades gracefully
// when they are absent, so they only warn.
var requiredTools = []tool{
	{
		name:        "go",
		label:       "Go toolchain",
		critical:    true,
		versionArgs: []string{"version"},
		installHint: "Install Go from https://go.dev/dl/ (version 1.21+ recommended).",
	},
	{
		name:        "node",
		label:       "Node.js",
		critical:    true,
		versionArgs: []string{"--version"},
		installHint: "Install Node.js from https://nodejs.org/ (LTS recommended) or via nvm/fnm.",
	},
	{
		name:        "git",
		label:       "Git",
		critical:    true,
		versionArgs: []string{"--version"},
		installHint: "Install Git from https://git-scm.com/downloads.",
	},
	{
		name:        "gopls",
		label:       "gopls (Go language server)",
		critical:    false,
		versionArgs: []string{"version"},
		installHint: "Install with: go install golang.org/x/tools/gopls@latest",
	},
	{
		name:        "tsserver",
		label:       "tsserver (TypeScript language server)",
		critical:    false,
		installHint: "Install TypeScript locally (npm i -D typescript) so node_modules/.bin/tsserver resolves, or install typescript-language-server globally: npm i -g typescript-language-server",
	},
	{
		name:        "typescript-language-server",
		label:       "typescript-language-server (alternative TS LSP wrapper)",
		critical:    false,
		versionArgs: []string{"--version"},
		installHint: "Optional LSP wrapper for TS/JS: npm i -g typescript-language-server",
	},
	{
		name:        "golangci-lint",
		label:       "golangci-lint (Go linter)",
		critical:    false,
		versionArgs: []string{"--version"},
		installHint: "Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest",
	},
	{
		name:        "eslint",
		label:       "eslint (JS/TS linter)",
		critical:    false,
		versionArgs: []string{"--version"},
		installHint: "Install with: npm i -g eslint",
	},
}

func main() {
	fmt.Println("Koyori IDE toolchain dependency check (G-OFF-02)")
	fmt.Println(strings.Repeat("=", 52))
	fmt.Println()

	missingCritical := []tool{}
	missingRecommended := []tool{}

	for _, t := range requiredTools {
		path, err := exec.LookPath(t.name)
		if err != nil {
			fmt.Printf("✗ %s — not found\n", t.label)
			if t.critical {
				missingCritical = append(missingCritical, t)
			} else {
				missingRecommended = append(missingRecommended, t)
			}
			continue
		}
		version := tryVersion(path, t.versionArgs...)
		if version != "" {
			fmt.Printf("✓ %s — %s (%s)\n", t.label, path, version)
		} else {
			fmt.Printf("✓ %s — %s\n", t.label, path)
		}
	}

	fmt.Println()
	fmt.Println(strings.Repeat("=", 52))

	// Recommended tools: warn only.
	if len(missingRecommended) > 0 {
		fmt.Println()
		fmt.Println("Recommended tools (missing — some language features will be degraded):")
		for _, t := range missingRecommended {
			fmt.Printf("  - %s\n", t.label)
			fmt.Printf("      %s\n", t.installHint)
		}
		fmt.Println()
		fmt.Println("The IDE still works offline without these; the affected languages")
		fmt.Println("fall back to basic editing (no LSP completion/diagnostics/linting).")
	}

	// Critical tools: fail the script.
	if len(missingCritical) > 0 {
		fmt.Println()
		fmt.Println("Critical tools (missing — required for core offline operation):")
		for _, t := range missingCritical {
			fmt.Printf("  - %s\n", t.label)
			fmt.Printf("      %s\n", t.installHint)
		}
		fmt.Println()
		fmt.Println("Install the missing critical tools, then re-run this check:")
		fmt.Println("  go run build/scripts/deps/install_deps.go")
		os.Exit(1)
	}

	if len(missingCritical) == 0 && len(missingRecommended) == 0 {
		fmt.Println()
		fmt.Println("✓ All toolchain dependencies are installed. The IDE is ready for")
		fmt.Println("  offline operation (editing, LSP, completion, Git, build, test, search).")
		return
	}

	fmt.Println()
	fmt.Println("✓ All critical dependencies are installed. The IDE is ready for core")
	fmt.Println("  offline operation. Install the recommended tools above to enable the")
	fmt.Println("  full language-feature set.")
}

// tryVersion runs `<exe> <args...>` and returns the trimmed first line of
// stdout. Returns "" if the command fails or produces no output.
func tryVersion(exe string, args ...string) string {
	if len(args) == 0 {
		return ""
	}
	cmd := exec.Command(exe, args...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[0])
}

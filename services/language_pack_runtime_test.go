package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func resignLanguagePackTestManifest(t *testing.T, raw []byte, mutate func(map[string]interface{})) []byte {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var manifest map[string]interface{}
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	mutate(manifest)
	delete(manifest, "integrity")
	canonical, err := canonicalJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(canonical))
	manifest["integrity"] = map[string]interface{}{"manifestSha256": hex.EncodeToString(digest[:])}
	result, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func embeddedLanguagePackTestBytes(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := builtInLanguagePackFiles.ReadFile("languagepacks/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLanguagePackRuntimeLoadsGoAndTypeScriptDefinitions(t *testing.T) {
	if len(builtInLanguagePacks) != 2 {
		t.Fatalf("built-in packs = %d, want 2", len(builtInLanguagePacks))
	}
	goDefinition, ok := lspDefinitionForLanguage("go")
	if !ok || goDefinition.sourcePackID != "org.koyori.ide.go" || goDefinition.sourcePackVersion != "1.0.0" {
		t.Fatalf("Go definition = %+v", goDefinition)
	}
	if len(goDefinition.candidates) != 1 || goDefinition.candidates[0].name != "gopls" || strings.Join(goDefinition.args, " ") != "serve" {
		t.Fatalf("Go language-pack command = %+v %v", goDefinition.candidates, goDefinition.args)
	}
	typescript, ok := lspDefinitionForLanguage("typescriptreact")
	if !ok || typescript.sourcePackID != "org.koyori.ide.typescript" || typescript.language != "typescript" {
		t.Fatalf("TypeScript definition = %+v", typescript)
	}
	if got := languagePackServerForPath(`C:\workspace\src\component.tsx`); got != "typescript" {
		t.Fatalf("TSX server = %q, want typescript", got)
	}
	if got := languagePackServerForPath("/workspace/scripts/tool.mjs"); got != "javascript" {
		t.Fatalf("MJS server = %q, want javascript", got)
	}
	if got := lspLanguageID("javascript", "/workspace/scripts/tool.jsx"); got != "javascriptreact" {
		t.Fatalf("JSX document language = %q", got)
	}
	if language, response, ok := languagePackConfiguration("gopls"); !ok || language != "go" || response != "full" {
		t.Fatalf("gopls configuration = %q/%q/%v", language, response, ok)
	}
	if builtInLanguagePacks[0].Toolchain == nil || len(builtInLanguagePacks[0].Toolchain.Commands) < 10 {
		t.Fatal("Go language pack must contribute structured toolchain commands")
	}
	if command, ok := toolchainCommandByID("go-build"); !ok || command.Command != "go" || strings.Join(command.Args, " ") != "build ./..." {
		t.Fatalf("Go build language-pack command = %+v/%v", command, ok)
	}
	if command, _ := toolchainCommandByID("go-build"); len(parseDiagnostics(command, "main.go:1:1: broken")) != 1 || parseDiagnostics(command, "main.go:1:1: broken")[0].Source != "go build" {
		t.Fatal("Go build diagnostic source changed during structured argv migration")
	}
	if command, ok := toolchainCommandByID("prettier-file"); !ok || command.Command != "prettier" || len(command.Args) != 1 || command.Args[0] != "--write" {
		t.Fatalf("Prettier language-pack command = %+v/%v", command, ok)
	}
	if !builtInLanguagePackToolchainCommandFileScoped("gofmt-file") || builtInLanguagePackToolchainCommandFileScoped("gofmt") {
		t.Fatal("language-pack file scope was not preserved")
	}
	goDebugger, ok := builtInLanguagePackDebuggerForLanguage("go")
	if !ok || goDebugger.ID != "delve" || goDebugger.Protocol != "dap" || goDebugger.Executable != "dlv" || strings.Join(goDebugger.Args, " ") != "dap --log=false" {
		t.Fatalf("Go language-pack debugger = %+v/%v", goDebugger, ok)
	}
	nodeDebugger, ok := builtInLanguagePackDebuggerForPath(`C:\workspace\src\component.tsx`)
	if !ok || nodeDebugger.ID != "node-inspector" || nodeDebugger.Protocol != "cdp" || nodeDebugger.Executable != "node" || strings.Join(nodeDebugger.Args, " ") != "--inspect-brk" {
		t.Fatalf("TypeScript language-pack debugger = %+v/%v", nodeDebugger, ok)
	}
	if _, ok := builtInLanguagePackDebuggerForPath("/workspace/README.md"); ok {
		t.Fatal("unowned language unexpectedly resolved a debugger")
	}
}

func TestLanguagePackRuntimeRejectsMalformedEnvelope(t *testing.T) {
	valid := embeddedLanguagePackTestBytes(t, "go.language-pack.json")
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{name: "unknown field", raw: []byte(strings.Replace(string(valid), `"displayName": "Go",`, `"displayName": "Go", "typo": true,`, 1)), want: "unknown field"},
		{name: "duplicate field", raw: []byte(strings.Replace(string(valid), `"schemaVersion": "1.0",`, `"schemaVersion": "1.0", "schemaVersion": "1.0",`, 1)), want: "duplicate JSON field"},
		{name: "digest mismatch", raw: []byte(strings.Replace(string(valid), `"displayName": "Go"`, `"displayName": "Changed"`, 1)), want: "SHA-256 does not match"},
		{name: "multiple values", raw: append(append([]byte(nil), valid...), []byte(` {}`)...), want: "multiple JSON values"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseLanguagePackManifest(test.raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLanguagePackRuntimeUsesStrictSemVer(t *testing.T) {
	valid := embeddedLanguagePackTestBytes(t, "go.language-pack.json")
	for _, version := range []string{"01.0.0", "1.0.0-01", "1.0.0-alpha..1", "1.0.0-"} {
		t.Run("reject_"+version, func(t *testing.T) {
			raw := resignLanguagePackTestManifest(t, valid, func(manifest map[string]interface{}) {
				manifest["version"] = version
			})
			if _, err := parseLanguagePackManifest(raw); err == nil || !strings.Contains(err.Error(), "invalid version") {
				t.Fatalf("parse version %q error = %v", version, err)
			}
		})
	}

	raw := resignLanguagePackTestManifest(t, valid, func(manifest map[string]interface{}) {
		manifest["version"] = "1.0.0-rc.1+windows.amd64"
	})
	if _, err := parseLanguagePackManifest(raw); err != nil {
		t.Fatalf("valid SemVer with build metadata rejected: %v", err)
	}
}

func TestLanguagePackRuntimeRejectsIncompatibleEngineHostAndPlatform(t *testing.T) {
	valid := embeddedLanguagePackTestBytes(t, "go.language-pack.json")
	otherOS := "windows"
	if runtime.GOOS == otherOS {
		otherOS = "linux"
	}
	tests := []struct {
		name   string
		mutate func(map[string]interface{})
		want   string
	}{
		{
			name: "engine api",
			mutate: func(manifest map[string]interface{}) {
				manifest["compatibility"].(map[string]interface{})["engineApi"] = "2.0"
			},
			want: "unsupported engine API",
		},
		{
			name: "host protocol",
			mutate: func(manifest map[string]interface{}) {
				manifest["compatibility"].(map[string]interface{})["hostProtocol"] = "language.remote.v1"
			},
			want: "unsupported host protocol",
		},
		{
			name: "current platform missing",
			mutate: func(manifest map[string]interface{}) {
				manifest["compatibility"].(map[string]interface{})["platforms"] = []interface{}{
					map[string]interface{}{"os": otherOS, "arch": runtime.GOARCH},
				}
			},
			want: "incompatible with platform",
		},
		{
			name: "unsupported platform",
			mutate: func(manifest map[string]interface{}) {
				manifest["compatibility"].(map[string]interface{})["platforms"] = []interface{}{
					map[string]interface{}{"os": "plan9", "arch": "amd64"},
				}
			},
			want: "unsupported operating system",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := resignLanguagePackTestManifest(t, valid, test.mutate)
			if _, err := parseLanguagePackManifest(raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLanguagePackRuntimeRejectsUnsafeServerAuthority(t *testing.T) {
	valid := embeddedLanguagePackTestBytes(t, "go.language-pack.json")
	tests := []struct {
		name   string
		mutate func(map[string]interface{})
		want   string
	}{
		{
			name: "absolute command path",
			mutate: func(manifest map[string]interface{}) {
				servers := manifest["servers"].([]interface{})
				executables := servers[0].(map[string]interface{})["executables"].([]interface{})
				executables[0].(map[string]interface{})["commandName"] = `C:\tools\gopls.exe`
			},
			want: "unsafe executable",
		},
		{
			name: "permission elevation",
			mutate: func(manifest map[string]interface{}) {
				manifest["permissions"] = []interface{}{"workspace.read", "process.launch", "workspace.write"}
			},
			want: "permissions must be exactly",
		},
		{
			name: "unknown language",
			mutate: func(manifest map[string]interface{}) {
				servers := manifest["servers"].([]interface{})
				servers[0].(map[string]interface{})["languages"] = []interface{}{"python"}
			},
			want: "unknown language",
		},
		{
			name: "unsupported profile",
			mutate: func(manifest map[string]interface{}) {
				servers := manifest["servers"].([]interface{})
				servers[0].(map[string]interface{})["initializationProfile"] = "arbitrary"
			},
			want: "unsupported initialization profile",
		},
		{
			name: "absolute version executable",
			mutate: func(manifest map[string]interface{}) {
				servers := manifest["servers"].([]interface{})
				servers[0].(map[string]interface{})["versionExecutable"] = `C:\tools\gopls.exe`
			},
			want: "servers[0] is invalid",
		},
		{
			name: "invalid version pin",
			mutate: func(manifest map[string]interface{}) {
				servers := manifest["servers"].([]interface{})
				servers[0].(map[string]interface{})["versionPin"] = "latest"
			},
			want: "servers[0] is invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := resignLanguagePackTestManifest(t, valid, test.mutate)
			if _, err := parseLanguagePackManifest(raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLanguagePackLSPVersionPinMatchesExactSemanticVersion(t *testing.T) {
	definition := lspServerDefinition{versionPin: "1.1.411"}
	if err := validateLSPServerVersionPin(definition, "pyright 1.1.411"); err != "" {
		t.Fatalf("matching version pin error = %q", err)
	}
	for _, output := range []string{"", "pyright 1.1.410", "pyright 1.1.4110", "pyright dev-1.1.411-beta"} {
		if err := validateLSPServerVersionPin(definition, output); err == "" {
			t.Fatalf("incompatible version %q was accepted", output)
		}
	}
}

func TestLanguagePackRuntimeRejectsUnsafeToolchainAuthority(t *testing.T) {
	valid := embeddedLanguagePackTestBytes(t, "go.language-pack.json")
	tests := []struct {
		name   string
		mutate func(map[string]interface{})
		want   string
	}{
		{
			name: "unknown language",
			mutate: func(manifest map[string]interface{}) {
				toolchain := manifest["toolchain"].(map[string]interface{})
				commands := toolchain["commands"].([]interface{})
				commands[0].(map[string]interface{})["language"] = "python"
			},
			want: "unknown language",
		},
		{
			name: "undeclared executable",
			mutate: func(manifest map[string]interface{}) {
				toolchain := manifest["toolchain"].(map[string]interface{})
				commands := toolchain["commands"].([]interface{})
				commands[0].(map[string]interface{})["executable"] = "sh"
			},
			want: "undeclared tool",
		},
		{
			name: "duplicate command id",
			mutate: func(manifest map[string]interface{}) {
				toolchain := manifest["toolchain"].(map[string]interface{})
				commands := toolchain["commands"].([]interface{})
				commands[1].(map[string]interface{})["id"] = commands[0].(map[string]interface{})["id"]
			},
			want: "repeats toolchain command",
		},
		{
			name: "nul argument",
			mutate: func(manifest map[string]interface{}) {
				toolchain := manifest["toolchain"].(map[string]interface{})
				commands := toolchain["commands"].([]interface{})
				commands[0].(map[string]interface{})["args"] = []interface{}{"\x00"}
			},
			want: "NUL",
		},
		{
			name: "empty install hint",
			mutate: func(manifest map[string]interface{}) {
				toolchain := manifest["toolchain"].(map[string]interface{})
				tools := toolchain["tools"].([]interface{})
				tools[0].(map[string]interface{})["installHint"] = ""
			},
			want: "unsafe tool declaration",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := resignLanguagePackTestManifest(t, valid, test.mutate)
			if _, err := parseLanguagePackManifest(raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLanguagePackRuntimeRejectsUnsafeDebuggerAuthority(t *testing.T) {
	valid := embeddedLanguagePackTestBytes(t, "go.language-pack.json")
	tests := []struct {
		name   string
		mutate func(map[string]interface{})
		want   string
	}{
		{
			name: "absolute executable",
			mutate: func(manifest map[string]interface{}) {
				debuggers := manifest["debuggers"].([]interface{})
				debuggers[0].(map[string]interface{})["executable"] = `C:\tools\dlv.exe`
			},
			want: "debuggers[0] is invalid",
		},
		{
			name: "unknown language",
			mutate: func(manifest map[string]interface{}) {
				debuggers := manifest["debuggers"].([]interface{})
				debuggers[0].(map[string]interface{})["languages"] = []interface{}{"python"}
			},
			want: "unknown language",
		},
		{
			name: "unsupported protocol",
			mutate: func(manifest map[string]interface{}) {
				debuggers := manifest["debuggers"].([]interface{})
				debuggers[0].(map[string]interface{})["protocol"] = "stdio"
			},
			want: "debuggers[0] is invalid",
		},
		{
			name: "nul argument",
			mutate: func(manifest map[string]interface{}) {
				debuggers := manifest["debuggers"].([]interface{})
				debuggers[0].(map[string]interface{})["args"] = []interface{}{"\x00"}
			},
			want: "NUL",
		},
		{
			name: "empty install hint",
			mutate: func(manifest map[string]interface{}) {
				debuggers := manifest["debuggers"].([]interface{})
				debuggers[0].(map[string]interface{})["installHint"] = ""
			},
			want: "debuggers[0] is invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := resignLanguagePackTestManifest(t, valid, test.mutate)
			if _, err := parseLanguagePackManifest(raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse error = %v, want %q", err, test.want)
			}
		})
	}
}

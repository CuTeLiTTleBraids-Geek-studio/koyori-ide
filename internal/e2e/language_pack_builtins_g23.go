//go:build e2e

package e2e

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services"
)

func (s *server) runLanguagePackBuiltinsG23Probe(cmd command) (interface{}, error) {
	if s.services.File == nil || s.services.LSP == nil || s.services.Toolchain == nil ||
		s.services.Debug == nil {
		return nil, errors.New("G23 built-in language pack automation is not fully wired")
	}
	if cmd.Workspace == "" || !filepath.IsAbs(cmd.Workspace) {
		return nil, errors.New("G23 built-in language pack probe requires an absolute workspace")
	}
	stage := func(name string) { slog.Info("G23 built-in language pack probe", "stage", name) }
	stage("prepare")

	frontendNodeModules := filepath.Join(mustWorkingDirectory(), "frontend", "node_modules")
	binDir := filepath.Join(frontendNodeModules, ".bin")
	tools := make(map[string]string, 4)
	for _, name := range []string{"prettier", "tsc", "typescript-language-server", "vitest"} {
		resolved, err := exec.LookPath(filepath.Join(binDir, name))
		if err != nil {
			return nil, fmt.Errorf("required real TypeScript tool %s is missing from %s", name, binDir)
		}
		tools[name] = resolved
	}
	restoreNodeModules, err := linkG23WorkspaceNodeModules(cmd.Workspace, frontendNodeModules)
	if err != nil {
		return nil, err
	}
	defer restoreNodeModules()

	mainPath := filepath.Join(cmd.Workspace, "main.go")
	originalMain, err := os.ReadFile(mainPath)
	if err != nil {
		return nil, fmt.Errorf("read Go editor fixture: %w", err)
	}
	defer os.WriteFile(mainPath, originalMain, 0o600)
	goSource := "package fixture\nimport \"fmt\"\nfunc main(){fmt.Println(\"g23\")}\n"
	if err := os.WriteFile(mainPath, []byte(goSource), 0o600); err != nil {
		return nil, err
	}
	goTestPath := filepath.Join(cmd.Workspace, "g23_language_pack_test.go")
	if err := os.WriteFile(goTestPath, []byte("package fixture\n\nimport \"testing\"\n\nfunc TestG23LanguagePack(t *testing.T) {}\n"), 0o600); err != nil {
		return nil, err
	}
	defer os.Remove(goTestPath)

	tsDir := filepath.Join(cmd.Workspace, "g23-typescript")
	if err := os.MkdirAll(tsDir, 0o755); err != nil {
		return nil, err
	}
	tsPath := filepath.Join(tsDir, "index.ts")
	tsTestPath := filepath.Join(tsDir, "index.test.ts")
	nodeDebugPath := filepath.Join(tsDir, "debug.js")
	if err := os.WriteFile(tsPath, []byte("export const add=(a:number,b:number)=>{return a+b}\n"), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(tsTestPath, []byte("import { expect, test } from 'vitest';\nimport { add } from './index';\ntest('adds', () => expect(add(20, 22)).toBe(42));\n"), 0o600); err != nil {
		return nil, err
	}
	nodeSource := "const inner = { z: 42 };\nconst outer = { inner };\ndebugger;\nconsole.log(outer.inner.z);\n"
	if err := os.WriteFile(nodeDebugPath, []byte(nodeSource), 0o600); err != nil {
		return nil, err
	}
	restoreRootFiles, err := writeG23RootTypeScriptFiles(cmd.Workspace)
	if err != nil {
		return nil, err
	}
	defer restoreRootFiles()

	for _, source := range []struct {
		command string
		packID  string
	}{
		{"gofmt-file", "org.koyori.ide.go"}, {"go-build", "org.koyori.ide.go"}, {"go-test", "org.koyori.ide.go"},
		{"prettier-file", "org.koyori.ide.typescript"}, {"tsc", "org.koyori.ide.typescript"}, {"vitest", "org.koyori.ide.typescript"},
	} {
		if !toolchainCommandFromPack(s.services.Toolchain, source.command, source.packID, "1.0.0") {
			return nil, fmt.Errorf("built-in toolchain command %s has no language pack source", source.command)
		}
	}
	goFormat, err := s.services.Toolchain.RunToolchainCommand("gofmt-file", mainPath)
	if err != nil || !goFormat.Success {
		return nil, fmt.Errorf("run Go format through language pack: result=%+v err=%v", goFormat, err)
	}
	formattedGo, err := os.ReadFile(mainPath)
	if err != nil || strings.Contains(string(formattedGo), "main(){") {
		return nil, fmt.Errorf("Go formatter did not update the fixture: %v", err)
	}
	goBuild, err := s.services.Toolchain.RunToolchainCommand("go-build", "")
	if err != nil || !goBuild.Success {
		return nil, fmt.Errorf("run Go build through language pack: result=%+v err=%v", goBuild, err)
	}
	goTest, err := s.services.Toolchain.RunToolchainCommand("go-test", "")
	if err != nil || !goTest.Success {
		return nil, fmt.Errorf("run Go test through language pack: result=%+v err=%v", goTest, err)
	}
	stage("go-toolchain-complete")

	tsFormat, err := s.services.Toolchain.RunToolchainCommand("prettier-file", tsPath)
	if err != nil || !tsFormat.Success {
		return nil, fmt.Errorf("run TypeScript format through language pack: result=%+v err=%v", tsFormat, err)
	}
	formattedTS, err := os.ReadFile(tsPath)
	if err != nil || strings.Contains(string(formattedTS), "add=(") {
		return nil, fmt.Errorf("TypeScript formatter did not update the fixture: %v", err)
	}
	tsBuild, err := s.services.Toolchain.RunToolchainCommand("tsc", "")
	if err != nil || !tsBuild.Success {
		return nil, fmt.Errorf("run TypeScript build through language pack: result=%+v err=%v", tsBuild, err)
	}
	tsTest, err := s.services.Toolchain.RunToolchainCommand("vitest", "")
	if err != nil || !tsTest.Success {
		return nil, fmt.Errorf("run TypeScript test through language pack: result=%+v err=%v", tsTest, err)
	}
	stage("typescript-toolchain-complete")

	if !lspStatusFromPack(s.services.LSP, "go", "org.koyori.ide.go", "1.0.0") ||
		!lspStatusFromPack(s.services.LSP, "typescript", "org.koyori.ide.typescript", "1.0.0") {
		return nil, errors.New("built-in LSP status source metadata diverged")
	}
	tsLSP, err := s.runLSPAction(command{
		Language: "typescript", Path: tsPath,
		Content:        "export const answer: number = 42;\nanswer.toF\n",
		CompletionLine: 1, CompletionColumn: 10, HoverLine: 0, HoverColumn: 13,
	})
	if err != nil {
		return nil, fmt.Errorf("run real TypeScript LSP: %w", err)
	}
	stage("typescript-lsp-complete")
	defer s.services.LSP.StopLSPServer("typescript")

	approvalProbe, restoreApproval, err := services.InstallDebugExecutableApprovalForE2E(
		s.services.Debug,
		"program",
		nodeDebugPath,
	)
	if err != nil {
		return nil, fmt.Errorf("install exact-path Node debug approval: %w", err)
	}
	defer restoreApproval()
	stage("node-debug-start")
	nodeInfo, err := s.services.Debug.LaunchNode(nodeDebugPath, nil)
	if err != nil {
		return nil, fmt.Errorf("launch real Node debugger: %w", err)
	}
	defer s.services.Debug.Stop()
	if !approvalProbe.Consumed() {
		return nil, errors.New("Node debugger bypassed the native executable approval boundary")
	}
	if nodeInfo.AdapterID != "node-inspector" || nodeInfo.SourcePackID != "org.koyori.ide.typescript" || nodeInfo.SourcePackVersion != "1.0.0" {
		return nil, fmt.Errorf("Node debugger source metadata diverged: %+v", nodeInfo)
	}
	entryDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(entryDeadline) && len(s.services.Debug.GetState().Stack) == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	if len(s.services.Debug.GetState().Stack) == 0 {
		return nil, errors.New("Node debugger did not produce an entry stack")
	}
	if err := s.services.Debug.Continue(); err != nil {
		return nil, fmt.Errorf("continue Node debugger: %w", err)
	}
	debuggerDeadline := time.Now().Add(20 * time.Second)
	valueFound := false
	for time.Now().Before(debuggerDeadline) {
		state := s.services.Debug.GetState()
		if state.Session.Stopped {
			for _, variable := range state.Locals {
				if variable.Name == "outer" && variable.VariablesReference > 0 {
					valueFound = true
				}
			}
			if valueFound {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !valueFound {
		return nil, errors.New("Node debugger did not expose the real TypeScript-pack fixture locals")
	}
	if err := s.services.Debug.Stop(); err != nil {
		return nil, fmt.Errorf("stop Node debugger: %w", err)
	}
	stage("node-debug-complete")

	return map[string]interface{}{
		"goLspSource": true, "goFormat": true, "goBuild": true, "goTest": true,
		"typescriptLsp": tsLSP, "typescriptFormat": true,
		"typescriptBuild": true, "typescriptTest": true, "typescriptDebug": true,
		"nativeDebugApprovalConsumed": true,
		"nodeAdapterId":               nodeInfo.AdapterID, "nodeSourcePackId": nodeInfo.SourcePackID,
		"nodeSourcePackVersion": nodeInfo.SourcePackVersion,
		"goFilePath":            mainPath, "typescriptFilePath": tsPath,
		"resolvedTools": map[string]string{
			"prettier": tools["prettier"], "tsc": tools["tsc"],
			"typescriptLanguageServer": tools["typescript-language-server"], "vitest": tools["vitest"],
		},
	}, nil
}

func mustWorkingDirectory() string {
	root, err := os.Getwd()
	if err != nil {
		return ""
	}
	return root
}

func linkG23WorkspaceNodeModules(workspace, target string) (func(), error) {
	link := filepath.Join(workspace, "node_modules")
	if _, err := os.Lstat(link); err == nil {
		return nil, fmt.Errorf("G23 workspace node_modules already exists: %s", link)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS != "windows" {
			return nil, fmt.Errorf("link G23 workspace node_modules: %w", err)
		}
		output, junctionErr := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput()
		if junctionErr != nil {
			return nil, fmt.Errorf("create G23 workspace node_modules junction: %w (%s)", junctionErr, strings.TrimSpace(string(output)))
		}
	}
	return func() { _ = os.Remove(link) }, nil
}

func writeG23RootTypeScriptFiles(root string) (func(), error) {
	type originalFile struct {
		path    string
		content []byte
		mode    os.FileMode
		exists  bool
	}
	fixtures := map[string]string{
		"package.json":  `{"name":"koyori-g23-fixture","private":true,"type":"module"}` + "\n",
		"tsconfig.json": `{"compilerOptions":{"strict":true,"target":"ES2022","module":"ESNext","moduleResolution":"Bundler","skipLibCheck":true},"include":["g23-typescript/index.ts"]}` + "\n",
	}
	originals := make([]originalFile, 0, len(fixtures))
	for name, content := range fixtures {
		path := filepath.Join(root, name)
		original := originalFile{path: path, mode: 0o600}
		if info, err := os.Stat(path); err == nil {
			original.exists = true
			original.mode = info.Mode().Perm()
			original.content, err = os.ReadFile(path)
			if err != nil {
				return nil, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		originals = append(originals, original)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return nil, err
		}
	}
	return func() {
		for _, original := range originals {
			if original.exists {
				_ = os.WriteFile(original.path, original.content, original.mode)
			} else {
				_ = os.Remove(original.path)
			}
		}
	}, nil
}

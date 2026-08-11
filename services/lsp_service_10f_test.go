package services

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestLSP10F_DetectReturnsBuiltInServers(t *testing.T) {
	statuses := NewLSPService("").DetectLSPServers()
	want := []string{"go", "typescript", "javascript", "json", "css", "html", "yaml", "eslint"}
	if len(statuses) != len(want) {
		t.Fatalf("DetectLSPServers returned %d entries, want %d: %+v", len(statuses), len(want), statuses)
	}
	for i, language := range want {
		if statuses[i].Language != language {
			t.Errorf("status[%d].Language = %q, want %q", i, statuses[i].Language, language)
		}
		if !statuses[i].Available && statuses[i].InstallHint == "" {
			t.Errorf("missing %s server has no actionable install hint", language)
		}
	}
}

func TestLSP10F_BuiltInServerArgv(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		language string
		kind     string
		exe      string
		wantArgs []string
	}{
		{"json", "vscode-json-languageserver", "json-ls", []string{"json-ls", "--stdio"}},
		{"css", "vscode-css-languageserver", "css-ls", []string{"css-ls", "--stdio"}},
		{"html", "vscode-html-languageserver", "html-ls", []string{"html-ls", "--stdio"}},
		{"yaml", "yaml-language-server", "yaml-ls", []string{"yaml-ls", "--stdio"}},
		{"eslint", "vscode-eslint-language-server", "eslint-ls", []string{"eslint-ls", "--stdio"}},
	}
	for _, tt := range tests {
		t.Run(tt.language, func(t *testing.T) {
			cmd, err := buildLSPServerCommand(tt.language, tt.exe, tt.kind, root)
			if err != nil {
				t.Fatalf("buildLSPServerCommand: %v", err)
			}
			if !reflect.DeepEqual(cmd.Args, tt.wantArgs) {
				t.Fatalf("argv = %#v, want %#v", cmd.Args, tt.wantArgs)
			}
			if cmd.Dir != root {
				t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, root)
			}
		})
	}
}

func TestLSP10F_MissingCommandsReturnInstallGuidance(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, language := range []string{"json", "css", "html", "yaml", "eslint"} {
		t.Run(language, func(t *testing.T) {
			err := NewLSPService(t.TempDir()).StartLSPServer(language)
			if err == nil {
				t.Fatal("StartLSPServer unexpectedly succeeded without command")
			}
			message := err.Error()
			if !strings.Contains(message, "npm install") || !strings.Contains(message, "language server not installed") {
				t.Fatalf("error is not actionable: %q", message)
			}
		})
	}
}

func TestLSP10F_LocalCommandDetection(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "node_modules", ".bin")
	if err := ensureTestDir(binDir); err != nil {
		t.Fatal(err)
	}
	name := "vscode-json-languageserver"
	contents := "#!/bin/sh\nexit 0\n"
	if runtime.GOOS == "windows" {
		name += ".cmd"
		contents = "@exit /b 0\r\n"
	}
	commandPath := filepath.Join(binDir, name)
	if err := writeExecutableTestFile(commandPath, contents); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	path, _, kind := detectServerPath("json", root)
	if path == "" {
		t.Fatal("workspace-local vscode-json-languageserver was not detected")
	}
	if kind != "vscode-json-languageserver" {
		t.Errorf("kind = %q, want vscode-json-languageserver", kind)
	}
}

func TestLSP10F_LanguageRouting(t *testing.T) {
	tests := []struct {
		uri      string
		server   string
		language string
	}{
		{"file:///workspace/tsconfig.json", "json", "json"},
		{"file:///workspace/settings.jsonc", "json", "jsonc"},
		{"file:///workspace/site.css", "css", "css"},
		{"file:///workspace/site.scss", "css", "scss"},
		{"file:///workspace/site.less", "css", "less"},
		{"file:///workspace/index.html", "html", "html"},
		{"file:///workspace/config.yml", "yaml", "yaml"},
		{"file:///workspace/config.yaml", "yaml", "yaml"},
	}
	for _, tt := range tests {
		t.Run(filepath.Base(tt.uri), func(t *testing.T) {
			server, err := languageFromURI(tt.uri)
			if err != nil {
				t.Fatalf("languageFromURI: %v", err)
			}
			if server != tt.server {
				t.Errorf("server = %q, want %q", server, tt.server)
			}
			if got := lspLanguageID(server, uriToPath(tt.uri)); got != tt.language {
				t.Errorf("languageId = %q, want %q", got, tt.language)
			}
		})
	}

	for _, filePath := range []string{"app.js", "app.jsx", "app.ts", "app.tsx", "component.vue"} {
		if got := lspLanguageID("eslint", filePath); got == "eslint" {
			t.Errorf("eslint route for %s did not preserve the document language", filePath)
		}
	}
}

func TestLSP10F_InitializeOptions(t *testing.T) {
	tests := []struct {
		language string
		assert   func(*testing.T, map[string]interface{})
	}{
		{"json", assertFormatterInitializationOption},
		{"css", assertFormatterInitializationOption},
		{"html", assertFormatterInitializationOption},
		{"yaml", func(t *testing.T, options map[string]interface{}) {
			if got, ok := options["isKubernetes"].(bool); !ok || got {
				t.Errorf("yaml isKubernetes = %#v, want false", options["isKubernetes"])
			}
		}},
		{"eslint", func(t *testing.T, options map[string]interface{}) {
			if options["run"] != "onType" || options["validate"] != "on" {
				t.Errorf("eslint options = %#v, want run=onType validate=on", options)
			}
			folder, ok := options["workspaceFolder"].(map[string]interface{})
			if !ok || folder["uri"] == "" || folder["name"] == "" {
				t.Errorf("eslint workspaceFolder = %#v", options["workspaceFolder"])
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.language, func(t *testing.T) {
			params := captureLSPInitializeParams(t, tt.language, t.TempDir())
			options, ok := params["initializationOptions"].(map[string]interface{})
			if !ok {
				t.Fatalf("initializationOptions = %#v, want object", params["initializationOptions"])
			}
			tt.assert(t, options)
		})
	}
}

func TestLSP10F_StopKeepsOtherServerRunning(t *testing.T) {
	svc := NewLSPService("")
	jsonServer := &lspServer{running: true}
	cssServer := &lspServer{running: true}
	svc.servers["json"] = jsonServer
	svc.servers["css"] = cssServer
	if err := svc.StopLSPServer("json"); err != nil {
		t.Fatalf("StopLSPServer(json): %v", err)
	}
	if _, ok := svc.servers["json"]; ok {
		t.Error("json server remained registered")
	}
	if svc.servers["css"] != cssServer || !cssServer.running {
		t.Error("stopping json changed the independent css server")
	}
}

func assertFormatterInitializationOption(t *testing.T, options map[string]interface{}) {
	t.Helper()
	if got, ok := options["provideFormatter"].(bool); !ok || !got {
		t.Errorf("provideFormatter = %#v, want true", options["provideFormatter"])
	}
}

func captureLSPInitializeParams(t *testing.T, language, root string) map[string]interface{} {
	t.Helper()
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	t.Cleanup(func() {
		_ = clientR.Close()
		_ = clientW.Close()
		_ = serverR.Close()
		_ = serverW.Close()
	})

	paramsCh := make(chan map[string]interface{}, 1)
	go func() {
		r := bufio.NewReader(serverR)
		body, err := readLSP10FFrame(r)
		if err != nil {
			return
		}
		var message map[string]interface{}
		if json.Unmarshal(body, &message) != nil {
			return
		}
		params, _ := message["params"].(map[string]interface{})
		paramsCh <- params
		response, _ := json.Marshal(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      message["id"],
			"result":  map[string]interface{}{"capabilities": map[string]interface{}{}},
		})
		_, _ = serverW.Write([]byte("Content-Length: " + strconv.Itoa(len(response)) + "\r\n\r\n"))
		_, _ = serverW.Write(response)
		// initializeLocked sends an initialized notification after the response.
		// Keep consuming until that write completes so io.Pipe cannot block it.
		_, _ = readLSP10FFrame(r)
	}()

	client := newJSONRPCClient(clientR, clientW)
	srv := &lspServer{client: client}
	svc := NewLSPService(root)
	if err := svc.initializeLocked(srv, language, root, nil); err != nil {
		t.Fatalf("initializeLocked: %v", err)
	}
	return <-paramsCh
}

func readLSP10FFrame(r *bufio.Reader) ([]byte, error) {
	length := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			length, err = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")))
			if err != nil {
				return nil, err
			}
		}
	}
	body := make([]byte, length)
	_, err := io.ReadFull(r, body)
	return body, err
}

func ensureTestDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func writeExecutableTestFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o755)
}

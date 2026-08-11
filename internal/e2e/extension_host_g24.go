//go:build e2e

package e2e

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
)

const (
	g24Publisher = "koyori-e2e-g24"
	g24Name      = "runtime-lifecycle"
)

// runExtensionHostG24Probe is a real packaged lifecycle transaction. The
// registry and VSIX payloads are served over loopback, while activation,
// Worker recovery, and renderer lifecycle acknowledgements stay on the normal
// product paths.
func (s *server) runExtensionHostG24Probe(_ command) (interface{}, error) {
	marketplace := s.services.Marketplace
	if marketplace == nil || s.services.ExecJS == nil {
		return nil, errors.New("G24 Extension Host automation is not fully wired")
	}

	v1, v1Hash, err := buildG24VSIX("1.0.0")
	if err != nil {
		return nil, fmt.Errorf("build G24 v1 VSIX: %w", err)
	}
	v2, v2Hash, err := buildG24VSIX("2.0.0")
	if err != nil {
		return nil, fmt.Errorf("build G24 v2 VSIX: %w", err)
	}
	var registry *httptest.Server
	registry = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + g24Publisher + "/" + g24Name:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"name":      g24Name,
				"namespace": g24Publisher,
				"versions": []map[string]interface{}{
					{"version": "2.0.0", "files": map[string]string{
						"download": registry.URL + "/v2.vsix", "sha256": v2Hash,
					}},
					{"version": "1.0.0", "files": map[string]string{
						"download": registry.URL + "/v1.vsix", "sha256": v1Hash,
					}},
				},
			})
		case "/v1.vsix":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(v1)
		case "/v2.vsix":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(v2)
		default:
			http.NotFound(w, r)
		}
	}))
	defer registry.Close()
	if err := marketplace.SetRegistryURLForE2E(registry.URL); err != nil {
		return nil, fmt.Errorf("configure G24 loopback registry: %w", err)
	}
	defer func() { _ = marketplace.SetRegistryURLForE2E("https://open-vsx.org/api") }()

	installed := false
	defer func() {
		if installed {
			_ = marketplace.UninstallExtension(g24Publisher, g24Name)
		}
	}()
	if err := marketplace.DownloadAndInstallExtension(g24Publisher, g24Name, "1.0.0"); err != nil {
		return nil, fmt.Errorf("install G24 v1: %w", err)
	}
	installed = true
	initial, err := marketplace.ListInstalledExtensions()
	if err != nil {
		return nil, fmt.Errorf("list G24 after install: %w", err)
	}
	if len(initial) != 1 || initial[0].Enabled || initial[0].Version != "1.0.0" {
		return nil, fmt.Errorf("G24 install did not start disabled at v1: %+v", initial)
	}
	installPath := initial[0].Path

	runPhase := func(phase, expectedVersion string) (map[string]interface{}, error) {
		runID, tokenErr := nextToken()
		if tokenErr != nil {
			return nil, tokenErr
		}
		configuration, marshalErr := json.Marshal(map[string]interface{}{
			"runId":           runID,
			"phase":           phase,
			"publisher":       g24Publisher,
			"name":            g24Name,
			"expectedVersion": expectedVersion,
		})
		if marshalErr != nil {
			return nil, marshalErr
		}
		value, probeErr := s.runRendererProbeWithExecutor(
			s.services.ExecJS,
			"__koyoriIdeRunG24ExtensionHostProbe",
			extensionHostG24ResultEvent,
			"G24 Extension Host",
			configuration,
		)
		if probeErr != nil {
			return nil, probeErr
		}
		result, ok := value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("G24 renderer result has unexpected type %T", value)
		}
		if result["ok"] != true {
			return result, fmt.Errorf("G24 renderer phase %s failed: %v", phase, result["error"])
		}
		return result, nil
	}

	v1Result, err := runPhase("activate-v1", "1.0.0")
	if err != nil {
		return nil, err
	}
	if err := marketplace.UpdateExtension(g24Publisher, g24Name, "2.0.0"); err != nil {
		return nil, fmt.Errorf("update G24 to v2: %w", err)
	}
	updated, err := marketplace.ListInstalledExtensions()
	if err != nil {
		return nil, fmt.Errorf("list G24 after update: %w", err)
	}
	if len(updated) != 1 || updated[0].Enabled || updated[0].Version != "2.0.0" {
		return nil, fmt.Errorf("G24 update did not commit disabled v2 state: %+v", updated)
	}

	v2Result, err := runPhase("activate-v2", "2.0.0")
	if err != nil {
		return nil, err
	}
	faultResult, err := runPhase("faults", "2.0.0")
	if err != nil {
		return nil, err
	}

	disabled, err := marketplace.ListInstalledExtensions()
	if err != nil {
		return nil, fmt.Errorf("list G24 after disable: %w", err)
	}
	if len(disabled) != 1 || disabled[0].Enabled {
		return nil, fmt.Errorf("G24 disable state was not persisted: %+v", disabled)
	}
	if err := marketplace.UninstallExtension(g24Publisher, g24Name); err != nil {
		return nil, fmt.Errorf("uninstall G24: %w", err)
	}
	installed = false
	remaining, err := marketplace.ListInstalledExtensions()
	if err != nil {
		return nil, fmt.Errorf("list G24 after uninstall: %w", err)
	}
	if len(remaining) != 0 {
		return nil, fmt.Errorf("G24 uninstall left installed state: %+v", remaining)
	}
	if _, statErr := os.Stat(installPath); !os.IsNotExist(statErr) {
		return nil, fmt.Errorf("G24 uninstall left extension directory: %v", statErr)
	}
	verifyResult, err := runPhase("verify-uninstalled", "2.0.0")
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"ok":                    true,
		"initialDisabled":       true,
		"v1Hash":                v1Hash,
		"v2Hash":                v2Hash,
		"v1Activation":          v1Result,
		"v2Activation":          v2Result,
		"faultIsolation":        faultResult,
		"uninstallVerification": verifyResult,
		"remainingInstalled":    len(remaining),
	}, nil
}

func buildG24VSIX(version string) ([]byte, string, error) {
	const commandPrefix = g24Publisher + "." + g24Name
	commands := []map[string]string{
		{"command": commandPrefix + ".version", "title": "G24 Version"},
		{"command": commandPrefix + ".permission", "title": "G24 Permission"},
		{"command": commandPrefix + ".forge", "title": "G24 Forged Token"},
		{"command": commandPrefix + ".crash", "title": "G24 Crash"},
		{"command": commandPrefix + ".hang", "title": "G24 Hang"},
		{"command": commandPrefix + ".flood", "title": "G24 Flood"},
		{"command": commandPrefix + ".oversize", "title": "G24 Oversize"},
	}
	manifest, err := json.Marshal(map[string]interface{}{
		"name":             g24Name,
		"publisher":        g24Publisher,
		"version":          version,
		"displayName":      "Koyori IDE G24 lifecycle fixture",
		"engines":          map[string]string{"vscode": "^1.80.0"},
		"activationEvents": []string{"onCommand:" + commandPrefix + ".version"},
		"main":             "./dist/main.js",
		"koyoriIde":        map[string]interface{}{"permissions": []string{}},
		"contributes":      map[string]interface{}{"commands": commands},
	})
	if err != nil {
		return nil, "", err
	}
	source := fmt.Sprintf(`const vscode = require("vscode");
const prefix = %q;
module.exports = {
  activate(context) {
    const register = (suffix, callback) => {
      context.subscriptions.push(vscode.commands.registerCommand(prefix + "." + suffix, callback));
    };
    register("version", () => %q);
    register("permission", async () => {
      try {
        await vscode.workspace.fs.readFile({ scheme: "file", fsPath: "C:/g24-permission-denied.txt" });
        return "unexpected-permission-success";
      } catch (error) {
        return "permission-denied:" + String(error && error.message ? error.message : error);
      }
    });
    register("forge", () => {
      globalThis.postMessage({ type: "rpc", token: "forged-token", id: 91, method: "commands.registerCommand", args: [prefix + ".forged", 999] });
      return "forged-sent";
    });
    register("crash", () => {
      setTimeout(() => { throw new Error("g24-runtime-crash"); }, 0);
      // WebView2 can report an uncaught Worker exception without terminating
      // the Dedicated Worker. Exit the fixture after the real crash attempt so
      // the host watchdog must observe and recover the dead runtime.
      setTimeout(() => { globalThis.close(); }, 25);
      return "crash-scheduled";
    });
    register("hang", () => {
      for (;;) {}
    });
    register("flood", () => {
      for (let index = 0; index < 1200; index += 1) {
        void vscode.commands.executeCommand(prefix + ".unknown").catch(() => undefined);
      }
      return "flood-sent";
    });
    register("oversize", () => vscode.workspace.fs.readFile({ scheme: "file", fsPath: "C:/" + "x".repeat(2_100_000) }));
  },
  deactivate() {}
};
`, commandPrefix, version)

	entries := []struct {
		name string
		data []byte
	}{
		{"extension/package.json", manifest},
		{"extension/dist/main.js", []byte(source)},
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, entry := range entries {
		writer, createErr := archive.Create(entry.name)
		if createErr != nil {
			return nil, "", createErr
		}
		if _, writeErr := writer.Write(entry.data); writeErr != nil {
			return nil, "", writeErr
		}
	}
	if err := archive.Close(); err != nil {
		return nil, "", err
	}
	data := buffer.Bytes()
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

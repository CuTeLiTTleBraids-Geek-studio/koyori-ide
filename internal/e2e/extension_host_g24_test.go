//go:build e2e

package e2e

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestBuildG24VSIXExposesWorkerRuntimeIdentity(t *testing.T) {
	archive, _, err := buildG24VSIX("2.0.0")
	if err != nil {
		t.Fatalf("buildG24VSIX: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("open VSIX: %v", err)
	}

	var manifestBytes, sourceBytes []byte
	for _, file := range reader.File {
		body, readErr := file.Open()
		if readErr != nil {
			t.Fatalf("open %s: %v", file.Name, readErr)
		}
		contents, readErr := io.ReadAll(body)
		closeErr := body.Close()
		if readErr != nil {
			t.Fatalf("read %s: %v", file.Name, readErr)
		}
		if closeErr != nil {
			t.Fatalf("close %s: %v", file.Name, closeErr)
		}
		switch file.Name {
		case "extension/package.json":
			manifestBytes = contents
		case "extension/dist/main.js":
			sourceBytes = contents
		}
	}
	if len(manifestBytes) == 0 || len(sourceBytes) == 0 {
		t.Fatal("VSIX is missing its manifest or runtime source")
	}

	var manifest struct {
		Contributes struct {
			Commands []struct {
				Command string `json:"command"`
			} `json:"commands"`
		} `json:"contributes"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	runtimeCommand := g24Publisher + "." + g24Name + ".runtime"
	found := false
	for _, command := range manifest.Contributes.Commands {
		if command.Command == runtimeCommand {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("manifest does not contribute %q", runtimeCommand)
	}
	source := string(sourceBytes)
	if !strings.Contains(source, `register("runtime"`) {
		t.Fatal("runtime command is not registered by the Worker fixture")
	}
	if !strings.Contains(source, "crypto.getRandomValues") {
		t.Fatal("Worker runtime identity is not unique per activation")
	}
}

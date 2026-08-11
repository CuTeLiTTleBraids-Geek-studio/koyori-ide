package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestG10RealGoplsCompletionAndHover(t *testing.T) {
	if os.Getenv("KOYORI_IDE_LSP_INTEGRATION") != "1" {
		t.Skip("set KOYORI_IDE_LSP_INTEGRATION=1 to run the real G10 LSP probe")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.25.0\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	filePath := filepath.Join(root, "main.go")
	content := "package fixture\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"ready\")\n\tfmt.Prin\n}\n"
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	service := NewLSPService(root)
	if err := service.StartLSPServer("go"); err != nil {
		t.Fatalf("start gopls: %v", err)
	}
	t.Cleanup(service.StopAll)

	request := LSPCompletionRequest{
		Language: "go",
		FilePath: filePath,
		Content:  content,
		Line:     6,
		Column:   9,
	}
	started := time.Now()
	items, err := service.GetCompletions(request)
	if err != nil {
		t.Fatalf("get completions: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("get completions returned no items, status=%+v", service.GetCallStatus("go"))
	}
	t.Logf("completion returned %d items after %s", len(items), time.Since(started))

	hoverRequest := request
	hoverRequest.Line = 5
	hoverRequest.Column = 8
	hover, err := service.GetHover(hoverRequest)
	if err != nil {
		t.Fatalf("get hover: %v", err)
	}
	if hover == "" {
		t.Fatal("get hover returned empty content")
	}
}

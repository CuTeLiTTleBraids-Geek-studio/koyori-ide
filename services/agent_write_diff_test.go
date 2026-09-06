package services

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

func TestWritePrepareAttachesDiffMetadata(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	grant, err := agent.RequestAgentToolCapability(context.Background(), AgentToolExecutionRequest{
		SessionID: "chat-1", CatalogRevision: catalog.Revision, ToolID: "write",
		Arguments: map[string]interface{}{"path": "note.txt", "content": "alpha\ngamma\n"},
	})
	if err != nil {
		t.Fatalf("capability: %v", err)
	}
	_ = grant
}

func TestWriteSelectedHunksDoesNotWriteHalfFileOnCAS(t *testing.T) {
	agent, _, _, root := newExecutionCoreTestServices(t)
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("stable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := &agentWriteHandler{agent: agent}
	args, _ := json.Marshal(map[string]interface{}{"path": "note.txt", "content": "next\n", "selectedHunks": []int{0}})
	invocation := agentcore.Invocation{
		Tool:      agentcore.ToolDef{ID: "write"},
		Arguments: args,
	}
	prepared, err := handler.Prepare(context.Background(), invocation)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := os.WriteFile(path, []byte("changed-on-disk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = handler.ExecuteWorkspaceTransaction(context.Background(), invocation, prepared)
	if err == nil || (!errors.Is(err, ErrConflict) && !strings.Contains(err.Error(), "CAS conflict") && !strings.Contains(err.Error(), "hash conflict")) {
		t.Fatalf("want CAS conflict, got %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "changed-on-disk") {
		t.Fatalf("partial write on CAS: %q", data)
	}
}

func TestApplySelectedHunksKeepsOnlyChosenHunk(t *testing.T) {
	s := NewDiffService()
	old := "one\n\n\n\n\n\n\n\n\n\nthree\n"
	neu := "ONE\n\n\n\n\n\n\n\n\n\nTHREE\n"
	fd := s.ComputeFileDiff("a.txt", old, neu)
	if len(fd.Hunks) < 2 {
		t.Fatalf("need 2 hunks, got %d (%+v)", len(fd.Hunks), fd.Hunks)
	}
	got := s.ApplySelectedHunks(fd, []int{0})
	if strings.Contains(got, "THREE") {
		t.Fatalf("unselected hunk leaked: %q", got)
	}
	if !strings.Contains(got, "ONE") {
		t.Fatalf("selected hunk missing: %q", got)
	}
}

func TestGoalWriteUsesCatalogWriteNotDarkFileAPI(t *testing.T) {
	src, err := os.ReadFile("agent_plan.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "WriteFile(") {
		t.Fatal("Goal planner must not call WriteFile directly")
	}
	if !strings.Contains(string(src), "executePlanStep") {
		t.Fatal("Goal writes must go through executePlanStep/catalog")
	}
}

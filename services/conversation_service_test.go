package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestConversationService_DefaultStorageResolvedBeforeFirstPath(t *testing.T) {
	services := map[string]*ConversationService{
		"constructor": NewConversationService(""),
		"zero-value":  {},
	}
	for name, svc := range services {
		t.Run(name, func(t *testing.T) {
			path, err := svc.pathFor("first-conversation")
			if err != nil {
				t.Fatalf("pathFor failed: %v", err)
			}
			if !filepath.IsAbs(path) {
				t.Fatalf("default conversation path = %q, want an absolute user-state path", path)
			}
			if filepath.Clean(path) == filepath.Clean("first-conversation.json") {
				t.Fatalf("default conversation path escaped to the process working directory: %q", path)
			}
		})
	}
}

func TestConversationService_DefaultStorageDoesNotReadOrWriteWorkingDirectory(t *testing.T) {
	workingDir := t.TempDir()
	configDir := t.TempDir()
	t.Chdir(workingDir)
	t.Setenv("APPDATA", configDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("HOME", configDir)

	const cwdOnlyID = "cwd-only"
	cwdOnlyPath := filepath.Join(workingDir, cwdOnlyID+".json")
	if err := os.WriteFile(cwdOnlyPath, []byte(`{"id":"cwd-only","title":"untrusted"}`), 0o600); err != nil {
		t.Fatalf("write cwd fixture: %v", err)
	}

	svc := NewConversationService("")
	if _, err := svc.Load(cwdOnlyID); err == nil {
		t.Fatal("default conversation service read a cwd-relative conversation")
	}

	const savedID = "saved-in-config"
	if err := svc.Save(Conversation{ID: savedID, Title: "Stored safely"}); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workingDir, savedID+".json")); !os.IsNotExist(err) {
		t.Fatalf("cwd conversation file exists or cannot be inspected: %v", err)
	}
	storedPath, err := svc.pathFor(savedID)
	if err != nil {
		t.Fatalf("pathFor saved conversation: %v", err)
	}
	if _, err := os.Stat(storedPath); err != nil {
		t.Fatalf("default state conversation missing at %q: %v", storedPath, err)
	}
}

func TestConversationService_Save_andLoad(t *testing.T) {
	dir := t.TempDir()
	svc := &ConversationService{storageDir: dir}

	conv := Conversation{
		ID:        "test-1",
		Title:     "Test conversation",
		CreatedAt: time.Now().Unix(),
		Messages: []ConversationMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi there"},
		},
	}

	err := svc.Save(conv)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	path := filepath.Join(dir, "test-1.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file at %s: %v", path, err)
	}

	loaded, err := svc.Load("test-1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Title != "Test conversation" {
		t.Errorf("expected title 'Test conversation', got %q", loaded.Title)
	}
	if len(loaded.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(loaded.Messages))
	}
	if loaded.Messages[1].Content != "hi there" {
		t.Errorf("expected second message 'hi there', got %q", loaded.Messages[1].Content)
	}
	if loaded.Revision != 1 {
		t.Errorf("expected revision 1 after first save, got %d", loaded.Revision)
	}
	if loaded.UpdatedAt == 0 {
		t.Error("expected UpdatedAt set after save")
	}
}

func TestConversationService_NativeToolRoundTrip(t *testing.T) {
	svc := &ConversationService{storageDir: t.TempDir()}
	conversation := Conversation{
		ID: "native-tool-round", Title: "Native tool round",
		Messages: []ConversationMessage{
			{Role: "user", Content: "Read README"},
			{
				Role: "assistant", Content: "",
				ToolCalls: []NativeToolCall{{
					ID: "call_read", Name: "read", Arguments: `{"path":"README.md"}`,
				}},
			},
			{
				Role: "tool", Content: "file body",
				ToolResults: []NativeToolResult{{
					ToolCallID: "call_read", Content: "file body",
				}},
			},
			{Role: "assistant", Content: "The file says hello."},
		},
	}
	if err := svc.Save(conversation); err != nil {
		t.Fatalf("Save native tool round: %v", err)
	}
	loaded, err := svc.Load(conversation.ID)
	if err != nil {
		t.Fatalf("Load native tool round: %v", err)
	}
	if len(loaded.Messages) != 4 || len(loaded.Messages[1].ToolCalls) != 1 || len(loaded.Messages[2].ToolResults) != 1 {
		t.Fatalf("native tool round did not survive reload: %+v", loaded.Messages)
	}
	if loaded.Messages[1].ToolCalls[0].ID != "call_read" ||
		loaded.Messages[2].ToolResults[0].ToolCallID != "call_read" {
		t.Fatalf("native tool identity drifted after reload: %+v", loaded.Messages)
	}
}

func TestConversationService_RejectsForgedNativeToolResults(t *testing.T) {
	svc := &ConversationService{storageDir: t.TempDir()}
	err := svc.Save(Conversation{
		ID: "orphan-result", Title: "invalid",
		Messages: []ConversationMessage{{
			Role:        "tool",
			ToolResults: []NativeToolResult{{ToolCallID: "missing", Content: "forged"}},
		}},
	})
	if err == nil {
		t.Fatal("Save accepted an orphan native tool result")
	}
	if _, statErr := os.Stat(filepath.Join(svc.storageDir, "orphan-result.json")); !os.IsNotExist(statErr) {
		t.Fatalf("invalid conversation reached disk: %v", statErr)
	}
}

// prompt-7 Task C / BUG-H6: CAS rejects stale ExpectedRevision.
func TestConversationService_Save_CASConflict(t *testing.T) {
	dir := t.TempDir()
	svc := &ConversationService{storageDir: dir}
	if err := svc.Save(Conversation{ID: "c1", Title: "v1", Messages: nil}); err != nil {
		t.Fatal(err)
	}
	loaded, err := svc.Load("c1")
	if err != nil {
		t.Fatal(err)
	}
	// Concurrent writer advances revision.
	if err := svc.Save(Conversation{ID: "c1", Title: "v2", Messages: nil}); err != nil {
		t.Fatal(err)
	}
	stale := loaded.Revision // 1
	base := stale
	err = svc.Save(Conversation{
		ID:               "c1",
		Title:            "stale",
		Messages:         nil,
		ExpectedRevision: &base,
	})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if !errors.Is(err, ErrConversationConflict) {
		t.Fatalf("want ErrConversationConflict, got %v", err)
	}
	// Matching base should succeed.
	cur, _ := svc.Load("c1")
	match := cur.Revision
	if err := svc.Save(Conversation{
		ID:               "c1",
		Title:            "v3",
		Messages:         nil,
		ExpectedRevision: &match,
	}); err != nil {
		t.Fatalf("matching CAS should succeed: %v", err)
	}
	final, _ := svc.Load("c1")
	if final.Title != "v3" || final.Revision != match+1 {
		t.Fatalf("got title=%q rev=%d", final.Title, final.Revision)
	}
}

func TestConversationService_Load_nonexistentReturnsError(t *testing.T) {
	dir := t.TempDir()
	svc := &ConversationService{storageDir: dir}

	_, err := svc.Load("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent conversation")
	}
}

func TestConversationService_List_returnsAllSortedByCreatedDesc(t *testing.T) {
	dir := t.TempDir()
	svc := &ConversationService{storageDir: dir}

	// Save three conversations with different timestamps
	for i, id := range []string{"a", "b", "c"} {
		err := svc.Save(Conversation{
			ID:        id,
			Title:     "Conv " + id,
			CreatedAt: int64(i + 1),
			Messages:  []ConversationMessage{},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	list, err := svc.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 conversations, got %d", len(list))
	}
	// Should be sorted by CreatedAt descending (newest first)
	if list[0].ID != "c" {
		t.Errorf("expected newest (c) first, got %q", list[0].ID)
	}
	if list[2].ID != "a" {
		t.Errorf("expected oldest (a) last, got %q", list[2].ID)
	}
}

func TestConversationService_List_appliesDefaultLimit(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)

	for i := 0; i < 55; i++ {
		if err := svc.Save(Conversation{
			ID:        fmt.Sprintf("conv-%03d", i),
			Title:     "Conversation",
			CreatedAt: int64(i + 1),
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := svc.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(got) != 50 {
		t.Fatalf("expected default limit of 50 conversations, got %d", len(got))
	}
}

func TestConversationService_ListWithFilter_appliesDefaultLimit(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)

	for i := 0; i < 55; i++ {
		if err := svc.Save(Conversation{
			ID:        fmt.Sprintf("match-%03d", i),
			Title:     "matching conversation",
			CreatedAt: int64(i + 1),
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := svc.ListWithFilter(ConversationFilter{Query: "matching"})
	if err != nil {
		t.Fatalf("ListWithFilter failed: %v", err)
	}
	if len(got) != 50 {
		t.Fatalf("expected default limit of 50 filtered conversations, got %d", len(got))
	}
}

func TestConversationService_ListPage_appliesOffsetLimitAndStableOrder(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)
	for _, id := range []string{"delta", "alpha", "charlie", "bravo"} {
		if err := svc.Save(Conversation{ID: id, Title: id, CreatedAt: 100}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := svc.ListPage(1, 2)
	if err != nil {
		t.Fatalf("ListPage failed: %v", err)
	}
	if len(got) != 2 || got[0].ID != "bravo" || got[1].ID != "charlie" {
		t.Fatalf("expected stable second page [bravo charlie], got %#v", got)
	}
}

func TestConversationService_ListPage_capsLimit(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)
	for i := 0; i < 205; i++ {
		if err := svc.Save(Conversation{
			ID:        fmt.Sprintf("conv-%03d", i),
			Title:     "Conversation",
			CreatedAt: int64(i + 1),
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := svc.ListPage(0, 500)
	if err != nil {
		t.Fatalf("ListPage failed: %v", err)
	}
	if len(got) != 200 {
		t.Fatalf("expected maximum limit of 200 conversations, got %d", len(got))
	}
}

func TestConversationService_ListWithFilterPage_filtersBeforePaging(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)
	if err := svc.Save(Conversation{ID: "wanted", Title: "needle", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 55; i++ {
		if err := svc.Save(Conversation{
			ID:        fmt.Sprintf("other-%03d", i),
			Title:     "other",
			CreatedAt: int64(i + 2),
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := svc.ListWithFilterPage(ConversationFilter{Query: "needle"}, 0, 1)
	if err != nil {
		t.Fatalf("ListWithFilterPage failed: %v", err)
	}
	if len(got) != 1 || got[0].ID != "wanted" {
		t.Fatalf("expected filtered page to contain wanted, got %#v", got)
	}
}

func TestConversationService_Delete_softDeletes(t *testing.T) {
	dir := t.TempDir()
	svc := &ConversationService{storageDir: dir}

	err := svc.Save(Conversation{
		ID:        "to-delete",
		Title:     "Delete me",
		CreatedAt: 1,
		Messages:  []ConversationMessage{},
	})
	if err != nil {
		t.Fatal(err)
	}

	err = svc.Delete("to-delete")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Plan 11 Task 2: Delete is now a soft-delete. The file must still exist
	// on disk (so it can be restored), but DeletedAt must be set.
	path := filepath.Join(dir, "to-delete.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("expected file to be retained for trash restore, but it was removed")
	}
	loaded, err := svc.Load("to-delete")
	if err != nil {
		t.Fatalf("Load after soft-delete failed: %v", err)
	}
	if loaded.DeletedAt == 0 {
		t.Error("expected DeletedAt to be set after soft-delete, got 0")
	}
	// Plan 11 Task 2: List() returns all conversations (including soft-deleted);
	// the frontend filters DeletedAt == 0 for the active list and DeletedAt > 0
	// for the trash view. ListWithFilter(IncludeTrash: false) excludes them.
	list, err := svc.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	var found bool
	for _, c := range list {
		if c.ID == "to-delete" {
			found = true
			if c.DeletedAt == 0 {
				t.Error("soft-deleted conversation in List() must carry DeletedAt")
			}
		}
	}
	if !found {
		t.Error("soft-deleted conversation should still appear in List() (frontend filters by DeletedAt)")
	}
	// ListWithFilter (default, IncludeTrash=false) must exclude soft-deleted.
	filtered, err := svc.ListWithFilter(ConversationFilter{})
	if err != nil {
		t.Fatalf("ListWithFilter failed: %v", err)
	}
	for _, c := range filtered {
		if c.ID == "to-delete" {
			t.Error("ListWithFilter(IncludeTrash=false) should exclude soft-deleted conversations")
		}
	}
}

func TestConversationService_GenerateID_isUnique(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := GenerateConversationID()
		if ids[id] {
			t.Errorf("duplicate ID generated: %s", id)
		}
		ids[id] = true
	}
}

func TestConversationService_GenerateTitleFromMessage(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Hello world", "Hello world"},
		{"This is a very long message that should be truncated because it exceeds the max title length", "This is a very long message that should be truncated because it e"},
		{"", "(new conversation)"},
		{"   ", "(new conversation)"},
	}
	for _, tt := range tests {
		got := GenerateTitle(tt.input)
		if got != tt.expected {
			t.Errorf("GenerateTitle(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// Ensure sort is stable for List — sanity check
func TestConversationService_List_emptyDirReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	svc := &ConversationService{storageDir: dir}
	list, err := svc.List()
	if err != nil {
		t.Fatalf("List on empty dir failed: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}
	_ = sort.IntSlice{} // keep sort import used
}

func TestConversationService_GenerateConversationID_Method(t *testing.T) {
	svc := &ConversationService{storageDir: t.TempDir()}
	id1 := svc.GenerateConversationID()
	id2 := svc.GenerateConversationID()
	if id1 == "" || id2 == "" {
		t.Error("GenerateConversationID should return non-empty string")
	}
	if id1 == id2 {
		t.Error("GenerateConversationID should return unique IDs")
	}
}

func TestConversationService_GenerateTitle_Method(t *testing.T) {
	svc := &ConversationService{storageDir: t.TempDir()}
	got := svc.GenerateTitle("Hello world")
	if got != "Hello world" {
		t.Errorf("GenerateTitle method = %q, want %q", got, "Hello world")
	}
	got = svc.GenerateTitle("")
	if got != "(new conversation)" {
		t.Errorf("GenerateTitle empty = %q, want %q", got, "(new conversation)")
	}
}

func TestConversationService_UpdateTitle(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)

	conv := Conversation{
		ID:        "test-conv-1",
		Title:     "Old Title",
		CreatedAt: time.Now().UnixMilli(),
		Messages:  []ConversationMessage{{Role: "user", Content: "hello"}},
	}
	if err := svc.Save(conv); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	if err := svc.UpdateTitle("test-conv-1", "New Title"); err != nil {
		t.Fatalf("UpdateTitle failed: %v", err)
	}

	loaded, err := svc.Load("test-conv-1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Title != "New Title" {
		t.Errorf("expected 'New Title', got %q", loaded.Title)
	}
	if len(loaded.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(loaded.Messages))
	}
}

// N-60: Per-conversation system prompt override persistence.
func TestConversationService_SystemPromptOverride_Persists(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)

	conv := Conversation{
		ID:                   "override-1",
		Title:                "Custom persona",
		CreatedAt:            time.Now().Unix(),
		Messages:             []ConversationMessage{{Role: "user", Content: "hi"}},
		SystemPromptOverride: "You are a strict code reviewer.",
	}
	if err := svc.Save(conv); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := svc.Load("override-1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.SystemPromptOverride != "You are a strict code reviewer." {
		t.Errorf("expected override to persist, got %q", loaded.SystemPromptOverride)
	}
}

// N-60: Empty override should be omitted from JSON (omitempty).
func TestConversationService_SystemPromptOverride_EmptyOmitted(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)

	conv := Conversation{
		ID:                   "no-override",
		Title:                "Default",
		CreatedAt:            1,
		Messages:             []ConversationMessage{},
		SystemPromptOverride: "",
	}
	if err := svc.Save(conv); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := svc.Load("no-override")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.SystemPromptOverride != "" {
		t.Errorf("expected empty override, got %q", loaded.SystemPromptOverride)
	}
}

// N-60: Loading a legacy conversation without the field should default to empty.
func TestConversationService_SystemPromptOverride_LegacyFileDefaultsEmpty(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)

	// Write a legacy JSON file without the system_prompt_override field.
	legacyJSON := `{"id":"legacy","title":"Legacy","created_at":1,"messages":[]}`
	path := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(path, []byte(legacyJSON), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	loaded, err := svc.Load("legacy")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.SystemPromptOverride != "" {
		t.Errorf("expected empty override for legacy file, got %q", loaded.SystemPromptOverride)
	}
}

func TestConversationService_UpdateTitle_NotFound(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)
	err := svc.UpdateTitle("nonexistent", "New Title")
	if err == nil {
		t.Fatal("expected error for nonexistent conversation")
	}
}

func TestConversationService_UpdateTitle_EmptyTitle(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)
	conv := Conversation{ID: "c1", Title: "Old", CreatedAt: time.Now().UnixMilli()}
	svc.Save(conv)
	err := svc.UpdateTitle("c1", "")
	if err == nil {
		t.Fatal("expected error for empty title")
	}
}

// N-91: Path traversal defense — Save/Load/Delete must reject ids that
// contain "..", path separators, or absolute paths. Previously, an id like
// "../../etc/evil" would read/write/delete arbitrary .json files outside
// the storage directory.
func TestConversationService_N91_Save_RejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)
	maliciousIDs := []string{
		"../evil",
		"..\\evil",
		"../../etc/passwd",
		"/etc/passwd",
		"sub/file",
		"sub\\file",
		".",
		"..",
	}
	for _, id := range maliciousIDs {
		t.Run(id, func(t *testing.T) {
			err := svc.Save(Conversation{
				ID:        id,
				Title:     "evil",
				CreatedAt: 1,
				Messages:  []ConversationMessage{{Role: "user", Content: "x"}},
			})
			if err == nil {
				t.Errorf("Save(%q) should reject path traversal, got nil", id)
			}
			// Verify no file was created outside the storage dir.
			target := filepath.Join(dir, id+".json")
			if _, err := os.Stat(target); err == nil {
				t.Errorf("file should not exist at %s", target)
			}
		})
	}
}

func TestConversationService_N91_Load_RejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)
	maliciousIDs := []string{
		"../evil",
		"..\\evil",
		"/etc/passwd",
		"sub/file",
		"..",
	}
	for _, id := range maliciousIDs {
		t.Run(id, func(t *testing.T) {
			_, err := svc.Load(id)
			if err == nil {
				t.Errorf("Load(%q) should reject path traversal, got nil", id)
			}
		})
	}
}

func TestConversationService_N91_Delete_RejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)
	// Place a file outside the storage dir to verify Delete does NOT remove it.
	parent := filepath.Dir(dir)
	outsideTarget := filepath.Join(parent, "evil.json")
	if err := os.WriteFile(outsideTarget, []byte("sensitive"), 0644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outsideTarget)

	err := svc.Delete("../evil")
	if err == nil {
		t.Error("Delete should reject path traversal, got nil")
	}
	// Verify the outside file is still there.
	if _, err := os.Stat(outsideTarget); err != nil {
		t.Errorf("outside file should still exist after rejected Delete: %v", err)
	}
}

// N-91: Sanity check — a legitimate id with a dash (the format produced by
// GenerateConversationID) is still accepted after the traversal fix.
func TestConversationService_N91_LegitimateIDStillWorks(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)
	id := svc.GenerateConversationID()
	conv := Conversation{
		ID:        id,
		Title:     "legit",
		CreatedAt: time.Now().Unix(),
		Messages:  []ConversationMessage{{Role: "user", Content: "hi"}},
	}
	if err := svc.Save(conv); err != nil {
		t.Fatalf("Save with generated ID failed: %v", err)
	}
	loaded, err := svc.Load(id)
	if err != nil {
		t.Fatalf("Load with generated ID failed: %v", err)
	}
	if loaded.ID != id {
		t.Errorf("expected ID %q, got %q", id, loaded.ID)
	}
	if err := svc.Delete(id); err != nil {
		t.Fatalf("Delete with generated ID failed: %v", err)
	}
}

// --- Plan 11 Task 2: Conversation organization & filtering ---

func TestConversationService_Task2_ListWithFilter_Query(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(svc.Save(Conversation{ID: "a", Title: "Refactor auth module", CreatedAt: 3,
		Messages: []ConversationMessage{{Role: "user", Content: "help me with login"}}}))
	must(svc.Save(Conversation{ID: "b", Title: "Chat about UI", CreatedAt: 2,
		Messages: []ConversationMessage{{Role: "user", Content: "make it pretty"}}}))
	must(svc.Save(Conversation{ID: "c", Title: "auth bug fix", CreatedAt: 1,
		Messages: []ConversationMessage{{Role: "user", Content: "token expires"}}}))

	// Query matches title.
	got, err := svc.ListWithFilter(ConversationFilter{Query: "auth"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 matches for 'auth', got %d", len(got))
	}
	// Query matches message content.
	got, err = svc.ListWithFilter(ConversationFilter{Query: "token"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "c" {
		t.Errorf("expected message-content match (c), got %v", got)
	}
	// Case-insensitive.
	got, err = svc.ListWithFilter(ConversationFilter{Query: "PRETTY"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "b" {
		t.Errorf("expected case-insensitive match (b), got %v", got)
	}
}

func TestConversationService_Task2_ListWithFilter_Metadata(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(svc.Save(Conversation{ID: "a", Title: "A", CreatedAt: 3, Tags: []string{"go"}, Favorite: true, Group: "work", PersonaID: "reviewer", Mode: "agent"}))
	must(svc.Save(Conversation{ID: "b", Title: "B", CreatedAt: 2, Tags: []string{"vue"}, Favorite: false, Group: "personal", Mode: "chat"}))

	fav := true
	got, err := svc.ListWithFilter(ConversationFilter{Favorite: &fav})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("favorite filter expected (a), got %v", got)
	}
	got, err = svc.ListWithFilter(ConversationFilter{Tag: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("tag filter expected (a), got %v", got)
	}
	got, err = svc.ListWithFilter(ConversationFilter{Group: "personal"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "b" {
		t.Errorf("group filter expected (b), got %v", got)
	}
	got, err = svc.ListWithFilter(ConversationFilter{PersonaID: "reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("persona filter expected (a), got %v", got)
	}
	got, err = svc.ListWithFilter(ConversationFilter{Mode: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("mode filter expected (a), got %v", got)
	}
}

func TestConversationService_Task2_Restore(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)
	if err := svc.Save(Conversation{ID: "r", Title: "Restore me", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete("r"); err != nil {
		t.Fatal(err)
	}
	// Default filter excludes it.
	got, err := svc.ListWithFilter(ConversationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		if c.ID == "r" {
			t.Fatal("soft-deleted should be excluded by default filter")
		}
	}
	// IncludeTrash shows it.
	got, err = svc.ListWithFilter(ConversationFilter{IncludeTrash: true})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range got {
		if c.ID == "r" {
			found = true
		}
	}
	if !found {
		t.Fatal("IncludeTrash should show soft-deleted conversation")
	}
	// Restore.
	if err := svc.Restore("r"); err != nil {
		t.Fatal(err)
	}
	loaded, err := svc.Load("r")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DeletedAt != 0 {
		t.Errorf("Restore should clear DeletedAt, got %d", loaded.DeletedAt)
	}
	// Default filter now includes it.
	got, err = svc.ListWithFilter(ConversationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var restored bool
	for _, c := range got {
		if c.ID == "r" {
			restored = true
		}
	}
	if !restored {
		t.Error("restored conversation should appear in default filter")
	}
}

func TestConversationService_Task2_PurgeExpiredTrash(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)
	// An old soft-deleted conversation (DeletedAt 40 days ago).
	old := time.Now().Add(-40 * 24 * time.Hour).Unix()
	if err := svc.Save(Conversation{ID: "old", Title: "old", CreatedAt: 1, DeletedAt: old}); err != nil {
		t.Fatal(err)
	}
	// A recent soft-deleted conversation (1 day ago).
	recent := time.Now().Add(-24 * time.Hour).Unix()
	if err := svc.Save(Conversation{ID: "recent", Title: "recent", CreatedAt: 2, DeletedAt: recent}); err != nil {
		t.Fatal(err)
	}
	// An active conversation.
	if err := svc.Save(Conversation{ID: "active", Title: "active", CreatedAt: 3}); err != nil {
		t.Fatal(err)
	}
	// Purge trash older than 30 days.
	purged, err := svc.PurgeExpiredTrash(30 * 24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 1 {
		t.Errorf("expected 1 purged, got %d", purged)
	}
	// old should be gone.
	if _, err := svc.Load("old"); err == nil {
		t.Error("old soft-deleted conversation should have been purged")
	}
	// recent should still exist.
	if _, err := svc.Load("recent"); err != nil {
		t.Error("recent soft-deleted conversation should still exist")
	}
	// active should still exist.
	if _, err := svc.Load("active"); err != nil {
		t.Error("active conversation should still exist")
	}
}

func TestConversationService_Task2_UpdateMetadata(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)
	if err := svc.Save(Conversation{ID: "m", Title: "Meta", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateTags("m", []string{"bug", "urgent"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateFavorite("m", true); err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateGroup("m", "sprint-1"); err != nil {
		t.Fatal(err)
	}
	loaded, err := svc.Load("m")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tags) != 2 || loaded.Tags[0] != "bug" {
		t.Errorf("tags mismatch: %v", loaded.Tags)
	}
	if !loaded.Favorite {
		t.Error("favorite should be true")
	}
	if loaded.Group != "sprint-1" {
		t.Errorf("group mismatch: %q", loaded.Group)
	}
}

// Plan 11 Task 2 Step 6: UpdateSortOrder persists the manual drag-and-drop
// order on the conversation, and the field round-trips through Save/Load.
func TestConversationService_Task2_UpdateSortOrder(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)
	if err := svc.Save(Conversation{ID: "s1", Title: "first", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Save(Conversation{ID: "s2", Title: "second", CreatedAt: 2}); err != nil {
		t.Fatal(err)
	}
	// Assign manual order: s2 -> 1, s1 -> 2 (reversed from created_at-desc).
	if err := svc.UpdateSortOrder("s2", 1); err != nil {
		t.Fatalf("UpdateSortOrder s2: %v", err)
	}
	if err := svc.UpdateSortOrder("s1", 2); err != nil {
		t.Fatalf("UpdateSortOrder s1: %v", err)
	}
	loaded1, err := svc.Load("s1")
	if err != nil {
		t.Fatal(err)
	}
	loaded2, err := svc.Load("s2")
	if err != nil {
		t.Fatal(err)
	}
	if loaded1.SortOrder != 2 {
		t.Errorf("s1 SortOrder = %d, want 2", loaded1.SortOrder)
	}
	if loaded2.SortOrder != 1 {
		t.Errorf("s2 SortOrder = %d, want 1", loaded2.SortOrder)
	}
	// Clearing the order (0) is supported — falls back to created_at-desc.
	if err := svc.UpdateSortOrder("s2", 0); err != nil {
		t.Fatalf("UpdateSortOrder clear: %v", err)
	}
	loaded2, _ = svc.Load("s2")
	if loaded2.SortOrder != 0 {
		t.Errorf("s2 SortOrder = %d, want 0 after clear", loaded2.SortOrder)
	}
}

// Plan 11 Task 2 Step 6: SortOrder is omitted from JSON when 0 (omitempty),
// so legacy conversations without the field load with SortOrder == 0.
func TestConversationService_Task2_SortOrder_LegacyOmitted(t *testing.T) {
	dir := t.TempDir()
	svc := NewConversationService(dir)
	// Legacy file without sort_order field.
	legacyJSON := `{"id":"legacy","title":"Legacy","created_at":1,"messages":[]}`
	path := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(path, []byte(legacyJSON), 0644); err != nil {
		t.Fatal(err)
	}
	loaded, err := svc.Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SortOrder != 0 {
		t.Errorf("legacy conversation SortOrder = %d, want 0", loaded.SortOrder)
	}
}

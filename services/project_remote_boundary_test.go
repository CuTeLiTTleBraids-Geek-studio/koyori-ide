package services

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// GOAL-P0-07A regression tests.
//
// Baseline defect: Project.Remote is deserialized from projects.json but neither
// AddProject nor GetRecentProjects was remote-aware. A remote entry's Path names
// a directory on the remote host, so resolving it against the local disk is
// wrong in two distinct ways:
//
//   - AddProject dispatched it into FileService / terminal / LSP / git / search
//     as if it were a local workspace. When a same-named directory happened to
//     exist locally, the user edited local files believing they were remote.
//   - GetRecentProjects stat-ed it locally, so Exists=true told the UI a remote
//     project was ready to open.
//
// Remote is an SSH/SFTP file-transfer and restricted-command surface. It has no
// remote PTY, language server, git, or debugger, so it must never enter the
// local IDE chain.

// writeProjectsFile seeds a projects.json so the test exercises the same load
// path production uses rather than reaching into unexported state.
func writeProjectsFile(t *testing.T, configPath string, projects []Project) {
	t.Helper()
	data, err := json.Marshal(projects)
	if err != nil {
		t.Fatalf("marshal projects: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write projects.json: %v", err)
	}
}

func TestAddProjectRefusesRemoteEntry(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "projects.json")
	// The remote path also exists locally — this is the dangerous case. Without
	// the guard the local directory is opened and silently edited.
	collidingPath := t.TempDir()
	writeProjectsFile(t, configPath, []Project{{
		ID:   "abc123",
		Name: "prod-box",
		Path: collidingPath,
		Remote: &RemoteConfig{
			Name:   "prod-box",
			Config: SSHConfig{Host: "10.0.0.5", Port: 22, User: "deploy"},
		},
	}})

	svc := &ProjectService{configPath: configPath}
	_, err := svc.AddProject(collidingPath)
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("AddProject(remote path) error = %v, want ErrNotAllowed", err)
	}
	// The message must name the boundary, not just refuse: the user needs to know
	// Remote is file transfer plus restricted commands, not a remote IDE.
	for _, want := range []string{"remote", "local workspace"} {
		if !containsFold(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

func TestAddProjectRemoteRefusalLeavesServiceRootsUntouched(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "projects.json")
	remotePath := t.TempDir()
	writeProjectsFile(t, configPath, []Project{{
		ID:     "abc123",
		Name:   "prod-box",
		Path:   remotePath,
		Remote: &RemoteConfig{Name: "prod-box", Config: SSHConfig{Host: "h", Port: 22, User: "u"}},
	}})

	// A real local workspace is open first, so the test can prove the rejected
	// switch does not disturb it.
	localWorkspace := t.TempDir()
	fs := &FileService{}
	wsCtx := NewWorkspaceContext()
	svc := &ProjectService{configPath: configPath, fileService: fs, wsCtx: wsCtx}
	if _, err := svc.AddProject(localWorkspace); err != nil {
		t.Fatalf("AddProject(local): %v", err)
	}
	wantRoot := wsCtx.Root()
	wantGen := wsCtx.Generation()

	if _, err := svc.AddProject(remotePath); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("AddProject(remote) error = %v, want ErrNotAllowed", err)
	}

	// The guard runs before Phase 1, so nothing may have been mutated and then
	// rolled back — the generation must not have moved at all.
	if got := wsCtx.Root(); got != wantRoot {
		t.Errorf("workspace root = %q after refusal, want %q", got, wantRoot)
	}
	if got := wsCtx.Generation(); got != wantGen {
		t.Errorf("generation = %d after refusal, want %d (guard must precede Phase 1)", got, wantGen)
	}
	roots := fs.WorkspaceRoots()
	if len(roots) != 1 || roots[0] != wantRoot {
		t.Errorf("FileService roots = %v after refusal, want [%s]", roots, wantRoot)
	}
}

func TestGetRecentProjectsMarksRemoteEntriesNotLocallyOpenable(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "projects.json")
	// Both paths exist locally. The remote entry must still report Exists=false.
	localPath := t.TempDir()
	remoteCollidingPath := t.TempDir()
	writeProjectsFile(t, configPath, []Project{
		{ID: "local1", Name: "local", Path: localPath, LastOpened: 2},
		{
			ID: "rem1", Name: "prod-box", Path: remoteCollidingPath, LastOpened: 1,
			Remote: &RemoteConfig{Name: "prod-box", Config: SSHConfig{Host: "h", Port: 22, User: "u"}},
		},
	})

	svc := &ProjectService{configPath: configPath}
	projects, err := svc.GetRecentProjects()
	if err != nil {
		t.Fatalf("GetRecentProjects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("got %d projects, want 2", len(projects))
	}

	byID := map[string]Project{}
	for _, proj := range projects {
		byID[proj.ID] = proj
	}

	local := byID["local1"]
	if !local.Exists {
		t.Error("local project Exists = false, want true")
	}
	if local.RemoteOnly {
		t.Error("local project RemoteOnly = true, want false")
	}

	remote := byID["rem1"]
	if remote.Exists {
		t.Error("remote project Exists = true; a locally-colliding path must not read as openable")
	}
	if !remote.RemoteOnly {
		t.Error("remote project RemoteOnly = false, want true so the UI can label the boundary")
	}
}

func TestAddProjectStillAcceptsLocalEntryWhenARemoteEntryExists(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "projects.json")
	remotePath := t.TempDir()
	writeProjectsFile(t, configPath, []Project{{
		ID:     "rem1",
		Name:   "prod-box",
		Path:   remotePath,
		Remote: &RemoteConfig{Name: "prod-box", Config: SSHConfig{Host: "h", Port: 22, User: "u"}},
	}})

	// The guard must match on path, not merely on "a remote entry exists".
	localPath := t.TempDir()
	svc := &ProjectService{configPath: configPath}
	proj, err := svc.AddProject(localPath)
	if err != nil {
		t.Fatalf("AddProject(local) with an unrelated remote entry present: %v", err)
	}
	if proj.Remote != nil {
		t.Error("newly added local project carries a Remote config")
	}
}

// containsFold reports whether s contains substr, case-insensitively, without
// pulling strings into this file's import set for a single assertion helper.
func containsFold(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	lower := func(r byte) byte {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			if lower(s[i+j]) != lower(substr[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

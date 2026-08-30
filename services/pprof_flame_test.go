package services

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSummarizeBuildsDeterministicFlameGraph(t *testing.T) {
	rp := &rawProfile{
		sampleTypes: []rawValueType{{typeIdx: 1, unitIdx: 2}},
		strings:     []string{"", "cpu", "nanoseconds", "root", "left", "right"},
		functions: map[uint64]*rawFunction{
			1: {id: 1, nameIdx: 3},
			2: {id: 2, nameIdx: 4},
			3: {id: 3, nameIdx: 5},
		},
		locations: map[uint64]*rawLocation{
			1: {id: 1, lines: []rawLine{{functionID: 1}}},
			2: {id: 2, lines: []rawLine{{functionID: 2}}},
			3: {id: 3, lines: []rawLine{{functionID: 3}}},
		},
		samples: []rawSample{
			{locationIDs: []uint64{2, 1}, values: []int64{70}},
			{locationIDs: []uint64{3, 1}, values: []int64{30}},
		},
	}

	analysis := summarize(rp)
	if analysis.FlameGraph == nil {
		t.Fatal("expected flame graph root")
	}
	root := analysis.FlameGraph
	if root.Name != "all" || root.Value != 100 || root.ID != "0" {
		t.Fatalf("unexpected flame root: %+v", root)
	}
	if len(root.Children) != 1 || root.Children[0].Name != "root" {
		t.Fatalf("expected merged root stack, got %+v", root.Children)
	}
	children := root.Children[0].Children
	if len(children) != 2 || children[0].Name != "left" || children[0].Value != 70 || children[1].Name != "right" || children[1].Value != 30 {
		t.Fatalf("expected value-sorted leaf frames, got %+v", children)
	}
	if children[0].ID == children[1].ID || children[0].ID == "" || children[1].ID == "" {
		t.Fatalf("frame IDs must be stable and unique: %+v", children)
	}
}

func TestBuildFlameGraphEnforcesNodeLimit(t *testing.T) {
	rp := &rawProfile{
		sampleTypes: []rawValueType{{typeIdx: 1, unitIdx: 2}},
		strings:     []string{"", "cpu", "nanoseconds"},
		functions:   map[uint64]*rawFunction{},
		locations:   map[uint64]*rawLocation{},
	}
	for index := 0; index < maxFlameGraphNodes+50; index++ {
		id := uint64(index + 1)
		rp.strings = append(rp.strings, "frame-"+string(rune(index+1)))
		rp.functions[id] = &rawFunction{id: id, nameIdx: int64(len(rp.strings) - 1)}
		rp.locations[id] = &rawLocation{id: id, lines: []rawLine{{functionID: id}}}
		rp.samples = append(rp.samples, rawSample{locationIDs: []uint64{id}, values: []int64{1}})
	}

	root := buildFlameGraph(rp, 0)
	if root == nil {
		t.Fatal("expected flame graph")
	}
	if count := countFlameNodes(root); count > maxFlameGraphNodes {
		t.Fatalf("flame graph node count = %d, limit = %d", count, maxFlameGraphNodes)
	}
	var childTotal int64
	var sawTruncated bool
	for _, child := range root.Children {
		childTotal += child.Value
		sawTruncated = sawTruncated || child.Name == truncatedFlameFrame
	}
	if childTotal != root.Value {
		t.Fatalf("root child total = %d, root value = %d", childTotal, root.Value)
	}
	if !sawTruncated {
		t.Fatal("expected overflow samples to be represented by a truncated frame")
	}
}

func countFlameNodes(node *FlameGraphNode) int {
	count := 1
	for index := range node.Children {
		count += countFlameNodes(&node.Children[index])
	}
	return count
}

func TestSummarizeDeduplicatesRecursiveAndInlineFrames(t *testing.T) {
	rp := &rawProfile{
		sampleTypes: []rawValueType{{typeIdx: 1, unitIdx: 2}},
		strings:     []string{"", "cpu", "nanoseconds", "recursive", "leaf", "inline-caller"},
		functions: map[uint64]*rawFunction{
			1: {id: 1, nameIdx: 3},
			2: {id: 2, nameIdx: 4},
			3: {id: 3, nameIdx: 5},
		},
		locations: map[uint64]*rawLocation{
			1: {id: 1, lines: []rawLine{{functionID: 2}, {functionID: 3}}},
			2: {id: 2, lines: []rawLine{{functionID: 1}}},
			3: {id: 3, lines: []rawLine{{functionID: 1}}},
		},
		samples: []rawSample{{locationIDs: []uint64{1, 2, 3}, values: []int64{10}}},
	}

	analysis := summarize(rp)
	byName := make(map[string]ProfileFunction, len(analysis.TopFunctions))
	for _, function := range analysis.TopFunctions {
		byName[function.Name] = function
	}
	if got := byName["recursive"].CumulativePercent; got != 100 {
		t.Fatalf("recursive cumulative percent = %v, want 100", got)
	}
	if got := byName["leaf"].FlatPercent; got != 100 {
		t.Fatalf("leaf flat percent = %v, want 100", got)
	}
	if got := byName["inline-caller"].FlatPercent; got != 0 {
		t.Fatalf("inline caller flat percent = %v, want 0", got)
	}
}

func TestPProfServiceBlockAndMutexProfiles(t *testing.T) {
	svc := newTestPProfService(t)

	blockPath := profilePath(svc, "block.prof")
	if err := svc.StartBlockProfile(); err != nil {
		t.Fatalf("StartBlockProfile: %v", err)
	}
	blockDone := make(chan struct{})
	blocked := make(chan struct{})
	go func() {
		close(blocked)
		<-blockDone
	}()
	<-blocked
	time.Sleep(20 * time.Millisecond)
	close(blockDone)
	time.Sleep(10 * time.Millisecond)
	if err := svc.StopBlockProfile(blockPath); err != nil {
		t.Fatalf("StopBlockProfile: %v", err)
	}
	assertNonEmptyFile(t, blockPath)

	mutexPath := profilePath(svc, "mutex.prof")
	if err := svc.StartMutexProfile(); err != nil {
		t.Fatalf("StartMutexProfile: %v", err)
	}
	var mu sync.Mutex
	mu.Lock()
	locked := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		close(locked)
		mu.Lock()
		runtime.Gosched()
		mu.Unlock()
		close(finished)
	}()
	<-locked
	time.Sleep(20 * time.Millisecond)
	mu.Unlock()
	<-finished
	if err := svc.StopMutexProfile(mutexPath); err != nil {
		t.Fatalf("StopMutexProfile: %v", err)
	}
	assertNonEmptyFile(t, mutexPath)
}

func TestSampleProfilesDisableBeforeReportingMissingOutput(t *testing.T) {
	svc := NewPProfService()
	if err := svc.StartBlockProfile(); err != nil {
		t.Fatal(err)
	}
	if err := svc.StopBlockProfile(""); err == nil || !strings.Contains(err.Error(), "output path") {
		t.Fatalf("StopBlockProfile error = %v, want output path error", err)
	}
	if active := svc.ActiveProfile(); active != "" {
		t.Fatalf("block profile remained active after stop error: %q", active)
	}

	if err := svc.StartMutexProfile(); err != nil {
		t.Fatal(err)
	}
	if err := svc.StopMutexProfile(""); err == nil || !strings.Contains(err.Error(), "output path") {
		t.Fatalf("StopMutexProfile error = %v, want output path error", err)
	}
	if active := svc.ActiveProfile(); active != "" {
		t.Fatalf("mutex profile remained active after stop error: %q", active)
	}
}

func TestCreateProfileFileRejectsExistingTargets(t *testing.T) {
	dir := t.TempDir()
	created := filepath.Join(dir, "created.prof")
	file, err := createProfileFile(created)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(created)
		if err != nil {
			t.Fatal(err)
		}
		if permissions := info.Mode().Perm(); permissions != 0o600 {
			t.Fatalf("profile permissions = %o, want 600", permissions)
		}
	}

	existing := filepath.Join(dir, "existing.prof")
	if err := os.WriteFile(existing, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if file, err := createProfileFile(existing); err == nil {
		_ = file.Close()
		t.Fatal("expected existing profile target to be rejected")
	}
	if got, err := os.ReadFile(existing); err != nil || string(got) != "keep" {
		t.Fatalf("existing target was modified: data=%q err=%v", got, err)
	}

	link := filepath.Join(dir, "profile-link")
	if err := os.Symlink(existing, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if file, err := createProfileFile(link); err == nil {
		_ = file.Close()
		t.Fatal("expected profile symlink target to be rejected")
	}
}

func TestTraceCommandTerminatesItsProcessTree(t *testing.T) {
	cmd := newTraceCommand(context.Background(), "go", "sched", "input.trace")
	if cmd.Cancel == nil {
		t.Fatal("trace command has no process-tree cancellation hook")
	}
	if cmd.WaitDelay <= 0 {
		t.Fatal("trace command has no bounded wait delay")
	}
}

func TestAnalyzeTraceRejectsNonRegularInput(t *testing.T) {
	_, err := NewPProfService().AnalyzeTrace(t.TempDir(), "sched")
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("AnalyzeTrace error = %v, want regular file rejection", err)
	}
}

func TestPProfServiceTraceCaptureAndSchedAnalysis(t *testing.T) {
	svc := newTestPProfService(t)
	tracePath := profilePath(svc, "runtime.trace")
	if err := svc.StartTrace(tracePath); err != nil {
		t.Fatalf("StartTrace: %v", err)
	}
	for i := 0; i < 100; i++ {
		runtime.Gosched()
	}
	if err := svc.StopTrace(); err != nil {
		t.Fatalf("StopTrace: %v", err)
	}
	assertNonEmptyFile(t, tracePath)

	analysis, err := svc.AnalyzeTrace(tracePath, "sched")
	if err != nil {
		t.Fatalf("AnalyzeTrace: %v", err)
	}
	if analysis.FlameGraph == nil {
		t.Fatal("trace analysis did not return a flame graph")
	}
	if _, err := svc.AnalyzeTrace(tracePath, "invalid"); err == nil {
		t.Fatal("expected invalid trace view to be rejected")
	}
}

func assertNonEmptyFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatalf("profile %s is empty", path)
	}
}

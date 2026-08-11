package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func waitForLocalWatchEvent(
	t *testing.T,
	events <-chan FileEvent,
	wantType string,
	wantPath string,
) FileEvent {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("watch channel closed before %s event for %s", wantType, wantPath)
			}
			if event.Type == wantType && filepath.Clean(event.Path) == filepath.Clean(wantPath) {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s event for %s", wantType, wantPath)
		}
	}
}

func TestLocalFileSystemWatchReportsNestedFileCreate(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "src", "internal")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := NewLocalFileSystem().Watch(ctx, root)
	if err != nil {
		t.Fatalf("watch root: %v", err)
	}

	target := filepath.Join(nested, "main.go")
	if err := os.WriteFile(target, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("create nested file: %v", err)
	}
	waitForLocalWatchEvent(t, events, "create", target)
}

func TestLocalFileSystemWatchAddsNewDirectories(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := NewLocalFileSystem().Watch(ctx, root)
	if err != nil {
		t.Fatalf("watch root: %v", err)
	}

	nested := filepath.Join(root, "generated")
	if err := os.Mkdir(nested, 0755); err != nil {
		t.Fatalf("create watched directory: %v", err)
	}
	waitForLocalWatchEvent(t, events, "create", nested)

	target := filepath.Join(nested, "schema.json")
	if err := os.WriteFile(target, []byte("{}\n"), 0644); err != nil {
		t.Fatalf("create file in new directory: %v", err)
	}
	waitForLocalWatchEvent(t, events, "create", target)
}

func TestPublishLocalFSNotifyEventMapsLifecycleOperations(t *testing.T) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("create watcher: %v", err)
	}
	defer func() {
		if err := watcher.Close(); err != nil {
			t.Fatalf("close watcher: %v", err)
		}
	}()

	ctx := context.Background()
	events := make(chan FileEvent, 3)
	path := filepath.Join(t.TempDir(), "file.go")
	deduper := newLocalWatchDeduper(localWatchDedupeWindow)
	now := time.Unix(100, 0)
	tests := []struct {
		op       fsnotify.Op
		wantType string
		oldPath  string
	}{
		{op: fsnotify.Write, wantType: "modify"},
		{op: fsnotify.Rename, wantType: "rename", oldPath: path},
		{op: fsnotify.Remove, wantType: "delete"},
	}

	for _, test := range tests {
		if ok := publishLocalFSNotifyEvent(
			ctx,
			watcher,
			filepath.Dir(filepath.Dir(path)),
			events,
			fsnotify.Event{Name: path, Op: test.op},
			deduper,
			now,
		); !ok {
			t.Fatalf("publish %s returned false", test.wantType)
		}
		event := <-events
		if event.Type != test.wantType || filepath.Clean(event.Path) != filepath.Clean(path) {
			t.Fatalf("%s event = %#v", test.wantType, event)
		}
		if event.OldPath != test.oldPath {
			t.Fatalf("%s oldPath = %q, want %q", test.wantType, event.OldPath, test.oldPath)
		}
	}
}

func TestLocalFSNotifyLoopExitsWhenEventChannelStaysFull(t *testing.T) {
	root := t.TempDir()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("create watcher: %v", err)
	}
	if err := watcher.Add(root); err != nil {
		watcher.Close()
		t.Fatalf("watch root: %v", err)
	}

	events := make(chan FileEvent, 1)
	events <- FileEvent{Type: "create", Path: "buffer-is-full"}
	exited := make(chan struct{})
	go func() {
		localFSNotifyLoop(context.Background(), watcher, root, events)
		close(exited)
	}()

	target := filepath.Join(root, "blocked.go")
	if err := os.WriteFile(target, []byte("package blocked\n"), 0644); err != nil {
		t.Fatalf("trigger file event: %v", err)
	}

	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("watch loop remained blocked after the event channel stayed full")
	}

	<-events
	if _, open := <-events; open {
		t.Fatal("event channel remained open after the watch loop exited")
	}
	if err := watcher.Add(root); !errors.Is(err, fsnotify.ErrClosed) {
		t.Fatalf("watcher remained open after blocked-channel exit: %v", err)
	}
}

func TestLocalWatchDeduperSuppressesOnlyIdenticalBurstEvents(t *testing.T) {
	deduper := newLocalWatchDeduper(100 * time.Millisecond)
	base := time.Unix(100, 0)
	path := filepath.Join("workspace", "main.go")
	modify := FileEvent{Type: "modify", Path: path}

	if !deduper.shouldPublish(modify, base) {
		t.Fatal("first modify event was suppressed")
	}
	if deduper.shouldPublish(modify, base.Add(10*time.Millisecond)) {
		t.Fatal("identical modify burst was not suppressed")
	}
	if !deduper.shouldPublish(FileEvent{Type: "delete", Path: path}, base.Add(20*time.Millisecond)) {
		t.Fatal("state transition from modify to delete was suppressed")
	}
	if !deduper.shouldPublish(modify, base.Add(100*time.Millisecond)) {
		t.Fatal("modify event remained suppressed after the dedupe window")
	}
}

func TestLocalWatchDeduperKeepsBoundedStateDuringUniqueBurst(t *testing.T) {
	deduper := newLocalWatchDeduper(100 * time.Millisecond)
	now := time.Unix(100, 0)
	for index := 0; index < localWatchDedupeMaxEntries+128; index++ {
		if !deduper.shouldPublish(FileEvent{
			Type: "modify",
			Path: filepath.Join("workspace", strconv.Itoa(index)),
		}, now) {
			t.Fatalf("unique event %d was suppressed", index)
		}
	}
	if len(deduper.last) > localWatchDedupeMaxEntries {
		t.Fatalf("dedupe state grew to %d entries", len(deduper.last))
	}
}

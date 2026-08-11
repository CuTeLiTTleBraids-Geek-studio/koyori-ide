package services

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setXdgCacheHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", dir)
	// Clear the per-user cache home override that adrg/xdg may have cached.
	// The xdg library re-reads env vars on each call to CacheHome, so this
	// is sufficient.
}

func TestInitLogger_CreatesLogFileAndWrites(t *testing.T) {
	setXdgCacheHome(t, t.TempDir())
	cleanup := InitLogger(slog.LevelInfo)
	defer cleanup()

	// Write a log entry.
	slog.Info("test message", "key", "value")

	// The log file should exist at the expected path.
	path := GetLogPath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected log file to exist at %s: %v", path, err)
	}

	// The log entry should be in the file.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}
	if !strings.Contains(string(data), "test message") {
		t.Errorf("log file does not contain the test message; got:\n%s", string(data))
	}
	if !strings.Contains(string(data), "key=value") {
		t.Errorf("log file does not contain the structured keyval; got:\n%s", string(data))
	}
}

func TestInitLogger_CleanupClosesFile(t *testing.T) {
	setXdgCacheHome(t, t.TempDir())
	cleanup := InitLogger(slog.LevelInfo)

	// Write something so the file has content.
	slog.Info("before cleanup")

	// Cleanup should close the file handle. After cleanup, calling it
	// again should be a no-op (no panic, no error).
	cleanup()
	cleanup() // second call must not panic
}

func TestGetLogPath_UnderXdgCacheHome(t *testing.T) {
	tmp := t.TempDir()
	setXdgCacheHome(t, tmp)

	path := GetLogPath()
	expected := filepath.Join(tmp, "koyori-ide", "koyori-ide.log")
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

func TestReadLog_ReturnsEmptyWhenFileMissing(t *testing.T) {
	setXdgCacheHome(t, t.TempDir())
	out, err := ReadLog(1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "" {
		t.Errorf("expected empty string, got %q", out)
	}
}

func TestReadLog_ReturnsFullContent(t *testing.T) {
	setXdgCacheHome(t, t.TempDir())
	cleanup := InitLogger(slog.LevelInfo)
	defer cleanup()

	slog.Info("first line")
	slog.Info("second line")

	out, err := ReadLog(64 * 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "first line") {
		t.Errorf("expected 'first line' in log; got:\n%s", out)
	}
	if !strings.Contains(out, "second line") {
		t.Errorf("expected 'second line' in log; got:\n%s", out)
	}
}

func TestReadLog_ReturnsTailWhenFileLargerThanMaxBytes(t *testing.T) {
	setXdgCacheHome(t, t.TempDir())
	cleanup := InitLogger(slog.LevelInfo)
	defer cleanup()

	// Write enough lines to exceed 200 bytes. Each slog.Info line is
	// typically 80-120 bytes, so 5 lines should be plenty.
	slog.Info("marker_early_line_that_should_be_dropped")
	for i := 0; i < 10; i++ {
		slog.Info("tail line that should be kept in the output window")
	}

	out, err := ReadLog(200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "marker_early_line_that_should_be_dropped") {
		t.Errorf("early marker should not be in the tail; got:\n%s", out)
	}
	if !strings.Contains(out, "tail line that should be kept") {
		t.Errorf("tail line should be in the output; got:\n%s", out)
	}
}

func TestReadLog_NegativeMaxBytesDefaultsTo64KiB(t *testing.T) {
	setXdgCacheHome(t, t.TempDir())
	cleanup := InitLogger(slog.LevelInfo)
	defer cleanup()

	slog.Info("some content")
	out, err := ReadLog(-1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "some content") {
		t.Errorf("expected 'some content' with default maxBytes; got:\n%s", out)
	}
}

func TestLogLevelService_GetLogPath(t *testing.T) {
	setXdgCacheHome(t, t.TempDir())
	svc := NewLogLevelService()
	got := svc.GetLogPath()
	expected := filepath.Join(GetLogPath())
	if got != expected {
		t.Errorf("expected %s, got %s", expected, got)
	}
}

func TestLogLevelService_ReadLogDelegates(t *testing.T) {
	setXdgCacheHome(t, t.TempDir())
	cleanup := InitLogger(slog.LevelInfo)
	defer cleanup()

	slog.Info("service delegate test")
	svc := NewLogLevelService()
	out, err := svc.ReadLog(0) // 0 should trigger default
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "service delegate test") {
		t.Errorf("expected delegate test message; got:\n%s", out)
	}
}

func TestFormatLogPath_HasFilePrefix(t *testing.T) {
	setXdgCacheHome(t, t.TempDir())
	got := FormatLogPath()
	if !strings.HasPrefix(got, "file://") {
		t.Errorf("expected file:// prefix, got %s", got)
	}
	if !strings.Contains(got, "koyori-ide.log") {
		t.Errorf("expected log file name in path, got %s", got)
	}
}

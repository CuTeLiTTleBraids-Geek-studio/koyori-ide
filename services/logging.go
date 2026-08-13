package services

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/adrg/xdg"
)

// LogPath is the absolute path to the application log file (N-11). It
// lives under the XDG cache directory so it persists across runs but is
// not backed up with user data.
const LogPath = "koyori-ide.log"

// logDirFn returns the directory that holds koyori-ide.log. It is a
// function variable (not a const) so tests can redirect it to a temp
// directory via t.Setenv("XDG_CACHE_HOME", ...) — the adrg/xdg library
// caches CacheHome at package init time on Windows, so we re-read the
// env var on each call and fall back to xdg.CacheHome when unset.
var logDirFn = func() string {
	if env := os.Getenv("XDG_CACHE_HOME"); env != "" {
		return filepath.Join(env, "koyori-ide")
	}
	return filepath.Join(xdg.CacheHome, "koyori-ide")
}

var (
	logFileMu sync.Mutex
	logFile   *os.File
)

// InitLogger configures the global slog default logger to write to a
// file under <xdg.CacheHome>/koyori-ide/koyori-ide.log (N-11). The
// file is opened in append mode so logs persist across runs. If the
// file cannot be opened, the default logger continues to write to
// stderr — logging is best-effort and never fatal.
//
// The returned cleanup function should be deferred by the caller to
// close the log file on shutdown. It is safe to call when no file was
// opened (it is a no-op then).
func InitLogger(level slog.Level) func() {
	logFileMu.Lock()
	defer logFileMu.Unlock()

	dir := logDirFn()
	if err := os.MkdirAll(dir, 0755); err != nil {
		// Fall back to the default logger (stderr).
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
		return func() {}
	}

	path := filepath.Join(dir, LogPath)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
		return func() {}
	}

	// N-104: close the previous log file handle before replacing it.
	// Without this, re-calling InitLogger (e.g. when the user changes
	// the log path at runtime) leaks the old fd until process exit.
	if logFile != nil {
		_ = logFile.Close()
	}
	logFile = f
	// Write to both the file and stderr so dev-mode console output is
	// preserved. A MultiWriter is fine here: logging is not on a hot
	// path (each service logs at most a handful of entries per call).
	handler := slog.NewTextHandler(io.MultiWriter(f, os.Stderr), &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
	slog.Info("logger initialized", "path", path, "level", level.String())
	return func() {
		logFileMu.Lock()
		defer logFileMu.Unlock()
		if logFile != nil {
			_ = logFile.Close()
			logFile = nil
		}
	}
}

// GetLogPath returns the absolute path to the application log file, or
// an empty string if the path cannot be determined. Used by the
// frontend's "View Log" button (N-11).
func GetLogPath() string {
	return filepath.Join(logDirFn(), LogPath)
}

// ReadLog returns the trailing contents of the application log file. If
// maxBytes is non-positive, a default of 64 KiB is used. If the file
// does not exist yet (no log has been written), an empty string is
// returned without error.
func ReadLog(maxBytes int) (string, error) {
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	path := GetLogPath()
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = f.Close()
	}()

	// If the file is larger than maxBytes, seek to (size - maxBytes) and
	// read only the tail. This keeps the "View Log" panel responsive
	// even when the file has grown large.
	offset := int64(0)
	if info.Size() > int64(maxBytes) {
		offset = info.Size() - int64(maxBytes)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// LogLevelService exposes runtime log configuration to the frontend
// (N-11). It is registered as a Wails service so the settings panel can
// show the log path and offer a "View Log" action.
type LogLevelService struct{}

// NewLogLevelService creates a LogLevelService.
func NewLogLevelService() *LogLevelService {
	return &LogLevelService{}
}

// GetLogPath returns the absolute path to the application log file.
func (s *LogLevelService) GetLogPath() string {
	return GetLogPath()
}

// ReadLog returns the tail of the application log (up to 64 KiB by
// default). Pass a positive maxBytes to override.
func (s *LogLevelService) ReadLog(maxBytes int) (string, error) {
	return ReadLog(maxBytes)
}

// FormatLogPath is a small helper for tests and diagnostics: it returns
// the log path with a leading "file://" prefix suitable for display.
func FormatLogPath() string {
	return fmt.Sprintf("file://%s", GetLogPath())
}

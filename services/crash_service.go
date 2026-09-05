package services

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// crash_service.go — 优先级 10: 崩溃报告。
//
// 通过 recover 捕获 panic，将崩溃信息（原因、堆栈、时间戳、应用版本、OS、
// 错误类型）写入崩溃目录（默认 ~/.koyori-ide/crashes/crash_<timestamp>.txt）。
// 用户可在设置中 opt-in 上报崩溃报告（当前仅记录日志，实际上传端点未配置）。
//
// 文件名校验使用 pathsec.ValidateNameForFlatDir 防止路径遍历攻击。

// crashesSubdir 是崩溃报告存放目录（相对于用户主目录）。
const crashesSubdir = ".koyori-ide/crashes"

// crashFilePrefix 是崩溃报告文件名前缀。
const crashFilePrefix = "crash_"

// CrashReport 描述一个崩溃报告（写入与读取的完整载荷）。
//
// 优先级 10 (prompt-1.md): 字段集对齐任务规范 —— Timestamp / Version / OS /
// Stack / Message / ErrorType。Filename 在读取单条报告时填充，写入时可选
// （为空则由 ReportCrash 按时间戳生成）。
type CrashReport struct {
	Filename  string    `json:"filename,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Version   string    `json:"version"`
	OS        string    `json:"os"`
	Stack     string    `json:"stack"`
	Message   string    `json:"message"`
	ErrorType string    `json:"errorType"`
}

// CrashReportInfo 是崩溃报告列表条目（仅元数据，不含堆栈正文）。
type CrashReportInfo struct {
	Filename  string    `json:"filename"`
	Timestamp time.Time `json:"timestamp"`
	Size      int64     `json:"size"`
}

// CrashService 负责写入、列出、读取、删除与上报崩溃报告。
//
// 崩溃报告默认存放于 ~/.koyori-ide/crashes/ 目录，文件名格式为
// crash_<unix-nano>.txt。dir 字段非空时覆盖默认目录（测试用）。
type CrashService struct {
	updateService *UpdateService
	dir           string
}

// NewCrashService 创建一个 CrashService。
// updateService 用于读取当前应用版本写入崩溃报告；为 nil 时版本字段为 "unknown"。
func NewCrashService(updateService *UpdateService) *CrashService {
	return &CrashService{updateService: updateService}
}

// setDir 覆盖崩溃报告目录（绝对路径）。主要用于测试以避免污染用户主目录。
//
//wails:ignore
func (s *CrashService) setDir(dir string) {
	s.dir = dir
}

// crashesDir 返回崩溃报告目录的绝对路径。dir 字段非空时直接使用；
// 否则默认为 ~/.koyori-ide/crashes。
func (s *CrashService) crashesDir() (string, error) {
	if s.dir != "" {
		return s.dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get user home dir: %w", err)
	}
	return filepath.Join(home, crashesSubdir), nil
}

// currentVersion 返回当前应用版本，updateService 为 nil 或读取失败时返回 "unknown"。
func (s *CrashService) currentVersion() string {
	if s.updateService == nil {
		return "unknown"
	}
	if v := s.updateService.GetCurrentVersion(); v != "" {
		return v
	}
	return "unknown"
}

// ReportCrash 将一条崩溃报告写入崩溃目录的 crash_<timestamp>.txt 文件。
// report.Timestamp 为零值时使用当前时间；Version/OS 为空时填充默认值；
// Filename 为空时按时间戳生成。即使写入失败也只返回错误，不再 panic
// （避免在 recover 路径中二次崩溃）。
func (s *CrashService) ReportCrash(report CrashReport) error {
	dir, err := s.crashesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create crashes dir: %w", err)
	}
	if report.Timestamp.IsZero() {
		report.Timestamp = time.Now()
	}
	if report.Version == "" {
		report.Version = s.currentVersion()
	}
	if report.OS == "" {
		report.OS = runtime.GOOS
	}
	filename := report.Filename
	if filename == "" {
		filename = fmt.Sprintf("%s%d.txt", crashFilePrefix, report.Timestamp.UnixNano())
	} else if err := ValidateNameForFlatDir(filename); err != nil {
		return fmt.Errorf("invalid crash filename: %w", err)
	} else if !strings.HasPrefix(filename, crashFilePrefix) || !strings.HasSuffix(filename, ".txt") {
		return fmt.Errorf("filename does not match crash report pattern")
	}
	path := filepath.Join(dir, filename)

	content := formatCrashReport(report)
	// 崩溃报告不含密钥，使用 0600 限制仅当前用户可读。
	if err := atomicWriteFile(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("write crash report: %w", err)
	}
	slog.Warn("crash report written", "path", path, "message", report.Message)
	return nil
}

// GetCrashReports 列出所有崩溃报告文件（仅元数据），按时间戳降序排列
// （最新在前）。无法解析时间戳的条目时间戳为零值并排在最后。
func (s *CrashService) GetCrashReports() ([]CrashReportInfo, error) {
	dir, err := s.crashesDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []CrashReportInfo{}, nil
		}
		return nil, fmt.Errorf("list crash reports: %w", err)
	}
	out := make([]CrashReportInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, crashFilePrefix) || !strings.HasSuffix(name, ".txt") {
			continue
		}
		info, serr := entry.Info()
		size := int64(0)
		if serr == nil {
			size = info.Size()
		}
		out = append(out, CrashReportInfo{
			Filename:  name,
			Timestamp: parseCrashTimestamp(name),
			Size:      size,
		})
	}
	// 按时间戳降序（最新在前）。
	sort.Slice(out, func(i, j int) bool {
		return out[i].Timestamp.After(out[j].Timestamp)
	})
	return out, nil
}

// GetCrashReport 读取指定崩溃报告文件并解析为 CrashReport。
// filename 必须是纯文件名（无路径分隔符、无 ".."），防止路径遍历。
func (s *CrashService) GetCrashReport(filename string) (*CrashReport, error) {
	if err := validateCrashFilename(filename); err != nil {
		return nil, err
	}
	dir, err := s.crashesDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, filename)
	if _, err := ValidatePathWithinRoot(dir, path); err != nil {
		return nil, fmt.Errorf("crash path validation failed: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read crash report: %w", err)
	}
	report := parseCrashReport(string(data))
	report.Filename = filename
	return &report, nil
}

// DeleteCrashReport 删除指定的崩溃报告文件。
// filename 必须是纯文件名（无路径分隔符、无 ".."），防止路径遍历。
func (s *CrashService) DeleteCrashReport(filename string) error {
	if err := validateCrashFilename(filename); err != nil {
		return err
	}
	dir, err := s.crashesDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, filename)
	// 二次校验：解析后的路径必须在崩溃目录内。
	if _, err := ValidateMutatingPathWithinRoot(dir, path); err != nil {
		return fmt.Errorf("crash path validation failed: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete crash report: %w", err)
	}
	return nil
}

// ClearAllCrashReports 删除所有崩溃报告文件。目录本身保留。
func (s *CrashService) ClearAllCrashReports() error {
	dir, err := s.crashesDir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("list crash reports for clear: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, crashFilePrefix) || !strings.HasSuffix(name, ".txt") {
			continue
		}
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete crash report %s: %w", name, err)
		}
	}
	return nil
}

// UploadCrash 上报指定的崩溃报告。
// 仅在用户启用崩溃上报时调用（由调用方检查设置）。当前实现只记录日志，
// 实际上传端点未配置。filename 必须是纯文件名，防止路径遍历。
func (s *CrashService) UploadCrash(filename string) error {
	if err := validateCrashFilename(filename); err != nil {
		return err
	}
	dir, err := s.crashesDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, filename)
	if _, err := ValidatePathWithinRoot(dir, path); err != nil {
		return fmt.Errorf("crash path validation failed: %w", err)
	}
	// 确认文件存在再记录日志。
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("crash report not found: %w", err)
	}
	slog.Info("crash report upload requested", "filename", filename,
		"note", "upload endpoint not configured; report retained locally")
	return nil
}

// validateCrashFilename 校验崩溃报告文件名：纯文件名且匹配 crash_*.txt 模式。
func validateCrashFilename(filename string) error {
	if err := ValidateNameForFlatDir(filename); err != nil {
		return fmt.Errorf("invalid crash filename: %w", err)
	}
	if !strings.HasPrefix(filename, crashFilePrefix) || !strings.HasSuffix(filename, ".txt") {
		return fmt.Errorf("filename does not match crash report pattern")
	}
	return nil
}

// formatCrashReport 将 CrashReport 序列化为可由 parseCrashReport 解析的文本。
func formatCrashReport(r CrashReport) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "koyori-ide crash report\n")
	fmt.Fprintf(&sb, "====================\n\n")
	fmt.Fprintf(&sb, "Timestamp: %s\n", r.Timestamp.Format(time.RFC3339Nano))
	fmt.Fprintf(&sb, "Version: %s\n", r.Version)
	fmt.Fprintf(&sb, "OS: %s\n", r.OS)
	fmt.Fprintf(&sb, "ErrorType: %s\n", r.ErrorType)
	fmt.Fprintf(&sb, "Message: %s\n", r.Message)
	fmt.Fprintf(&sb, "\nStack Trace:\n%s\n", r.Stack)
	return sb.String()
}

// parseCrashTimestamp 从文件名 crash_<unix-nano>.txt 解析时间戳。
// 解析失败返回零值时间。
func parseCrashTimestamp(name string) time.Time {
	// 去掉前缀 "crash_" 与后缀 ".txt"。
	core := strings.TrimPrefix(name, crashFilePrefix)
	core = strings.TrimSuffix(core, ".txt")
	var nanos int64
	if _, err := fmt.Sscanf(core, "%d", &nanos); err != nil {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

// parseCrashReport 从文本内容解析 CrashReport。
// 解析键值对行（Timestamp / Version / OS / ErrorType / Message），
// Stack Trace 之后的多行内容作为 Stack 字段。
func parseCrashReport(content string) CrashReport {
	report := CrashReport{}
	lines := strings.Split(content, "\n")
	stackStart := -1
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "Timestamp: "):
			ts := strings.TrimPrefix(line, "Timestamp: ")
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				report.Timestamp = t
			} else if t, err := time.Parse(time.RFC3339, ts); err == nil {
				report.Timestamp = t
			}
		case strings.HasPrefix(line, "Version: "):
			report.Version = strings.TrimPrefix(line, "Version: ")
		case strings.HasPrefix(line, "OS: "):
			report.OS = strings.TrimPrefix(line, "OS: ")
		case strings.HasPrefix(line, "ErrorType: "):
			report.ErrorType = strings.TrimPrefix(line, "ErrorType: ")
		case strings.HasPrefix(line, "Message: "):
			report.Message = strings.TrimPrefix(line, "Message: ")
		case strings.HasPrefix(line, "Stack Trace:"):
			stackStart = i + 1
		}
	}
	if stackStart >= 0 && stackStart < len(lines) {
		stack := strings.Join(lines[stackStart:], "\n")
		// 去掉末尾空行。
		report.Stack = strings.TrimRight(stack, "\n")
	}
	return report
}

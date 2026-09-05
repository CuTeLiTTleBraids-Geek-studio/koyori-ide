package services

// pprof_service_test.go — Priority 7 测试。
// 注意：测试名沿用任务规范要求的 TestProfileService_P7_* 前缀
// （被测服务为 PProfService，与 profile_service.go 的 ProfileService 区分）。

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newTestPProfService 构造一个隔离的 PProfService 实例，输出沙箱根指向
// 独立临时目录（P19 P1-02：profile 输出必须落在工作区内）。
func newTestPProfService(t *testing.T) *PProfService {
	t.Helper()
	s := NewPProfService()
	s.setWorkspaceRoot(t.TempDir())
	return s
}

// profilePath 返回服务沙箱根下的 profile 输出路径。
func profilePath(s *PProfService, name string) string {
	return filepath.Join(s.currentWorkspaceRoot(), name)
}

// cpuBurn 占用 CPU 约 d 时长，确保 CPU 采样能采集到样本。
func cpuBurn(d time.Duration) {
	deadline := time.Now().Add(d)
	x := 0
	for time.Now().Before(deadline) {
		for i := 0; i < 1000; i++ {
			x += i * i
		}
	}
	_ = x
}

// blockGoroutine 启动一个会阻塞至返回 stop 的 goroutine，制造一个
// 稳定存在于 goroutine profile 中的栈帧，保证分析结果非空。
func blockGoroutine() func() {
	done := make(chan struct{})
	go func() {
		<-done
	}()
	// 给调度器一点时间让 goroutine 真正起来并阻塞。
	time.Sleep(20 * time.Millisecond)
	return func() { close(done) }
}

func TestProfileService_P7_StartStopCPUProfile(t *testing.T) {
	s := newTestPProfService(t)
	out := profilePath(s, "cpu.prof")

	if s.IsProfiling() {
		t.Fatal("new service should not be profiling")
	}
	if err := s.StartCPUProfile(out); err != nil {
		t.Fatalf("StartCPUProfile: %v", err)
	}
	if !s.IsProfiling() {
		t.Fatal("IsProfiling should be true after start")
	}
	// 稍作 CPU 占用，确保 profile 头部写入。
	cpuBurn(60 * time.Millisecond)
	if err := s.StopCPUProfile(); err != nil {
		t.Fatalf("StopCPUProfile: %v", err)
	}
	if s.IsProfiling() {
		t.Fatal("IsProfiling should be false after stop")
	}
	if _, err := os.Stat(out); os.IsNotExist(err) {
		t.Fatal("cpu profile file was not created")
	}
}

func TestProfileService_P7_CaptureHeapProfile(t *testing.T) {
	s := newTestPProfService(t)
	out := profilePath(s, "heap.prof")

	// 分配一些对象以产生堆数据。
	_ = make([][]byte, 8)
	for i := range make([]struct{}, 8) {
		_ = make([]byte, 1<<16)
		_ = i
	}
	runtime.GC()

	if err := s.CaptureHeapProfile(out); err != nil {
		t.Fatalf("CaptureHeapProfile: %v", err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("heap profile file stat: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("heap profile file is empty")
	}
}

func TestProfileService_P7_CaptureGoroutineProfile(t *testing.T) {
	s := newTestPProfService(t)
	out := profilePath(s, "goroutine.prof")

	stop := blockGoroutine()
	defer stop()

	// debug=0 输出二进制 pprof 格式（可供 AnalyzeProfile 解析）。
	if err := s.CaptureGoroutineProfile(out, 0); err != nil {
		t.Fatalf("CaptureGoroutineProfile: %v", err)
	}
	if _, err := os.Stat(out); os.IsNotExist(err) {
		t.Fatal("goroutine profile file was not created")
	}
}

func TestProfileService_P7_AnalyzeProfile(t *testing.T) {
	s := newTestPProfService(t)
	out := profilePath(s, "analyze.prof")

	// 用 goroutine 快照作为分析样本：进程内总有 goroutine，
	// 解析后 TopFunctions 必然非空（避免短时 CPU 采样无样本的偶发性）。
	stop := blockGoroutine()
	defer stop()

	if err := s.CaptureGoroutineProfile(out, 0); err != nil {
		t.Fatalf("CaptureGoroutineProfile: %v", err)
	}
	analysis, err := s.AnalyzeProfile(out)
	if err != nil {
		t.Fatalf("AnalyzeProfile: %v", err)
	}
	if analysis == nil {
		t.Fatal("analysis is nil")
	}
	if analysis.TotalSamples <= 0 {
		t.Fatalf("expected TotalSamples > 0, got %d", analysis.TotalSamples)
	}
	if len(analysis.TopFunctions) == 0 {
		t.Fatalf("expected TopFunctions populated, got empty (samples=%d)", analysis.TotalSamples)
	}
	// 校验每个热点函数字段合理性。
	var sawNonEmpty bool
	for _, fn := range analysis.TopFunctions {
		if fn.Name == "" {
			t.Errorf("function name is empty: %+v", fn)
		}
		if fn.CumulativePercent < 0 || fn.CumulativePercent > 100 {
			t.Errorf("CumulativePercent out of range: %f for %s", fn.CumulativePercent, fn.Name)
		}
		if fn.Name != "" {
			sawNonEmpty = true
		}
	}
	if !sawNonEmpty {
		t.Fatal("no function with a non-empty name was found")
	}
}

func TestProfileService_AnalyzeProfileRejectsInputOver256MiB(t *testing.T) {
	s := newTestPProfService(t)
	out := profilePath(s, "oversized.prof")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{7}); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Truncate(256*1024*1024 + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = s.AnalyzeProfile(out)
	if err == nil || !strings.Contains(err.Error(), "256 MiB") {
		t.Fatalf("AnalyzeProfile error = %v, want explicit 256 MiB limit error", err)
	}
}

func TestProfileService_P7_StartTwiceError(t *testing.T) {
	s := newTestPProfService(t)
	out := profilePath(s, "twice.prof")

	if err := s.StartCPUProfile(out); err != nil {
		t.Fatalf("first StartCPUProfile: %v", err)
	}
	// 清理：无论断言是否通过都停止，避免进程级 CPU 采样残留影响后续测试。
	t.Cleanup(func() {
		if s.IsProfiling() {
			_ = s.StopCPUProfile()
		}
	})

	err := s.StartCPUProfile(out)
	if err == nil {
		t.Fatal("expected error when starting CPU profile twice, got nil")
	}
}

// TestProfileService_OutputFailsClosedWithoutWorkspaceRoot 验证未链接工作区
// 根时（应用启动后、未打开项目前）一切 profile 输出 fail-closed。
func TestProfileService_OutputFailsClosedWithoutWorkspaceRoot(t *testing.T) {
	s := NewPProfService()
	out := filepath.Join(t.TempDir(), "heap.prof")

	if err := s.CaptureHeapProfile(out); err == nil || !strings.Contains(err.Error(), "rejected by workspace sandbox") {
		t.Fatalf("CaptureHeapProfile without workspace root: err=%v, want sandbox rejection", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("profile file must not be created when the sandbox root is unset")
	}
}

// TestProfileService_RejectsOutputOutsideWorkspaceRoot 验证沙箱根之外的
// 绝对路径被拒绝且未落盘。
func TestProfileService_RejectsOutputOutsideWorkspaceRoot(t *testing.T) {
	s := newTestPProfService(t)
	outside := filepath.Join(t.TempDir(), "escape.prof")

	if err := s.CaptureHeapProfile(outside); err == nil || !strings.Contains(err.Error(), "rejected by workspace sandbox") {
		t.Fatalf("CaptureHeapProfile outside root: err=%v, want sandbox rejection", err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatal("profile file must not be created outside the workspace root")
	}
}

// TestProfileService_RejectsDotDotTraversalOutput 验证以 ".." 回溯逃逸
// 沙箱根的路径被拒绝且未落盘。
func TestProfileService_RejectsDotDotTraversalOutput(t *testing.T) {
	s := newTestPProfService(t)
	escape := filepath.Join(s.currentWorkspaceRoot(), "..", "escape.prof")

	if err := s.CaptureHeapProfile(escape); err == nil || !strings.Contains(err.Error(), "rejected by workspace sandbox") {
		t.Fatalf("CaptureHeapProfile with dot-dot traversal: err=%v, want sandbox rejection", err)
	}
	escaped := filepath.Join(filepath.Dir(s.currentWorkspaceRoot()), "escape.prof")
	if _, err := os.Stat(escaped); !os.IsNotExist(err) {
		t.Fatal("profile file must not be created outside the workspace root")
	}
}

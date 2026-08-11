package services

// pprof_service.go — Priority 7 (prompt-1.md 422-432): Go pprof 性能分析集成。
// 提供 CPU / Heap / Goroutine 采样与 pprof 二进制 profile 解析能力。
//
// 注意：本服务与 profile_service.go（用户多配置文件管理，Plan 50）概念不同，
// 后者管理 <configDir>/koyori-ide/profiles/<name>/；本服务管理 runtime/pprof
// 性能采样。故命名为 PProfService 并独立成文件，避免命名冲突。

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"runtime/pprof"
	runtimeTrace "runtime/trace"
	"sort"
	"strings"
	"sync"
	"time"
)

// PProfService 封装 runtime/pprof 采样能力（Priority 7）。
// 通过 Wails 暴露给前端 ProfilePanel：开始/停止 CPU 采样、抓取堆与
// goroutine 快照、解析已有 profile 文件并返回热点函数摘要。
type PProfService struct {
	mu sync.Mutex

	// CPU 采样状态
	cpuProfiling  bool
	cpuFile       *os.File
	cpuStart      time.Time
	activeProfile string
	traceFile     *os.File
	mutexFraction int
}

// NewPProfService 构造性能分析服务。
func NewPProfService() *PProfService {
	return &PProfService{}
}

// StartCPUProfile 开始 runtime/pprof CPU 采样，输出写入 outputPath。
// 若已在采样则返回错误。outputPath 的父目录需已存在。
func (s *PProfService) StartCPUProfile(outputPath string) error {
	if outputPath == "" {
		return fmt.Errorf("output path is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeProfile != "" {
		return fmt.Errorf("%s profiling is already active", s.activeProfile)
	}
	f, err := createProfileFile(outputPath)
	if err != nil {
		return fmt.Errorf("create cpu profile file: %w", err)
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("start cpu profile: %w", err)
	}
	s.cpuProfiling = true
	s.activeProfile = "cpu"
	s.cpuFile = f
	s.cpuStart = time.Now()
	return nil
}

// StopCPUProfile 停止 CPU 采样并关闭文件。未在采样时返回错误。
func (s *PProfService) StopCPUProfile() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cpuProfiling {
		return fmt.Errorf("CPU profiling is not active")
	}
	pprof.StopCPUProfile()
	f := s.cpuFile
	s.cpuProfiling = false
	s.activeProfile = ""
	s.cpuFile = nil
	s.cpuStart = time.Time{}
	if f != nil {
		if err := f.Close(); err != nil {
			return fmt.Errorf("close cpu profile file: %w", err)
		}
	}
	return nil
}

// IsProfiling 返回 CPU 采样是否正在进行。
func (s *PProfService) IsProfiling() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cpuProfiling
}

// ActiveProfile returns the currently running profile session.
func (s *PProfService) ActiveProfile() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeProfile
}

// CaptureHeapProfile 使用 pprof.WriteHeapProfile 将当前堆 profile 写入 outputPath。
func (s *PProfService) CaptureHeapProfile(outputPath string) error {
	if outputPath == "" {
		return fmt.Errorf("output path is empty")
	}
	f, err := createProfileFile(outputPath)
	if err != nil {
		return fmt.Errorf("create heap profile file: %w", err)
	}
	defer f.Close()
	if err := pprof.WriteHeapProfile(f); err != nil {
		return fmt.Errorf("write heap profile: %w", err)
	}
	return nil
}

// CaptureGoroutineProfile 将 goroutine profile 写入 outputPath。
// 使用 pprof.Lookup("goroutine").WriteTo。
// debug 为 0 时输出二进制格式，>0 时输出可读文本（1=单行/栈，2=同 1 且带运行时元信息）。
func (s *PProfService) CaptureGoroutineProfile(outputPath string, debug int) error {
	if outputPath == "" {
		return fmt.Errorf("output path is empty")
	}
	p := pprof.Lookup("goroutine")
	if p == nil {
		return fmt.Errorf("goroutine profile not available")
	}
	f, err := createProfileFile(outputPath)
	if err != nil {
		return fmt.Errorf("create goroutine profile file: %w", err)
	}
	defer f.Close()
	if err := p.WriteTo(f, debug); err != nil {
		return fmt.Errorf("write goroutine profile: %w", err)
	}
	return nil
}

// StartBlockProfile enables runtime block profiling until StopBlockProfile.
func (s *PProfService) StartBlockProfile() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeProfile != "" {
		return fmt.Errorf("%s profiling is already active", s.activeProfile)
	}
	runtime.SetBlockProfileRate(1)
	s.activeProfile = "block"
	return nil
}

// StopBlockProfile disables block profiling and writes a binary pprof file.
func (s *PProfService) StopBlockProfile(outputPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeProfile != "block" {
		return fmt.Errorf("block profiling is not active")
	}
	runtime.SetBlockProfileRate(0)
	s.activeProfile = ""
	if outputPath == "" {
		return fmt.Errorf("output path is empty; block profiling was stopped without saving")
	}
	return writeRuntimeProfile(outputPath, "block")
}

// StartMutexProfile enables collection of every contended mutex event.
func (s *PProfService) StartMutexProfile() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeProfile != "" {
		return fmt.Errorf("%s profiling is already active", s.activeProfile)
	}
	s.mutexFraction = runtime.SetMutexProfileFraction(1)
	s.activeProfile = "mutex"
	return nil
}

// StopMutexProfile restores the prior sampling fraction and writes pprof data.
func (s *PProfService) StopMutexProfile(outputPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeProfile != "mutex" {
		return fmt.Errorf("mutex profiling is not active")
	}
	runtime.SetMutexProfileFraction(s.mutexFraction)
	s.mutexFraction = 0
	s.activeProfile = ""
	if outputPath == "" {
		return fmt.Errorf("output path is empty; mutex profiling was stopped without saving")
	}
	return writeRuntimeProfile(outputPath, "mutex")
}

// StartTrace starts a runtime trace session writing directly to outputPath.
func (s *PProfService) StartTrace(outputPath string) error {
	if outputPath == "" {
		return fmt.Errorf("output path is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeProfile != "" {
		return fmt.Errorf("%s profiling is already active", s.activeProfile)
	}
	f, err := createProfileFile(outputPath)
	if err != nil {
		return fmt.Errorf("create trace file: %w", err)
	}
	if err := runtimeTrace.Start(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("start trace: %w", err)
	}
	s.traceFile = f
	s.activeProfile = "trace"
	return nil
}

// StopTrace stops the active runtime trace and closes its output file.
func (s *PProfService) StopTrace() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeProfile != "trace" {
		return fmt.Errorf("trace profiling is not active")
	}
	runtimeTrace.Stop()
	f := s.traceFile
	s.traceFile = nil
	s.activeProfile = ""
	if f != nil {
		if err := f.Close(); err != nil {
			return fmt.Errorf("close trace file: %w", err)
		}
	}
	return nil
}

// Close finalizes any active runtime profile. It is safe to call repeatedly.
func (s *PProfService) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	switch s.activeProfile {
	case "cpu":
		pprof.StopCPUProfile()
		if s.cpuFile != nil {
			err = s.cpuFile.Close()
		}
	case "trace":
		runtimeTrace.Stop()
		if s.traceFile != nil {
			err = s.traceFile.Close()
		}
	case "block":
		runtime.SetBlockProfileRate(0)
	case "mutex":
		runtime.SetMutexProfileFraction(s.mutexFraction)
	}
	s.cpuProfiling = false
	s.cpuFile = nil
	s.cpuStart = time.Time{}
	s.traceFile = nil
	s.mutexFraction = 0
	s.activeProfile = ""
	if err != nil {
		return fmt.Errorf("close active profile: %w", err)
	}
	return nil
}

func createProfileFile(path string) (*os.File, error) {
	// Profiles contain source paths and stack data. Refuse every existing
	// target, including symlinks, so a capture cannot overwrite or follow one.
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
}

func writeRuntimeProfile(outputPath, name string) error {
	profile := pprof.Lookup(name)
	if profile == nil {
		return fmt.Errorf("%s profile not available", name)
	}
	f, err := createProfileFile(outputPath)
	if err != nil {
		return fmt.Errorf("create %s profile file: %w", name, err)
	}
	defer f.Close()
	if err := profile.WriteTo(f, 0); err != nil {
		return fmt.Errorf("write %s profile: %w", name, err)
	}
	return nil
}

// ProfileFunction 是单个函数的采样摘要。
type ProfileFunction struct {
	Name              string        `json:"name"`
	CumulativeTime    time.Duration `json:"cumulativeTime"`
	FlatTime          time.Duration `json:"flatTime"`
	CumulativePercent float64       `json:"cumulativePercent"`
	FlatPercent       float64       `json:"flatPercent"`
}

// FlameGraphNode is one aggregated call-path frame in a profile.
type FlameGraphNode struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Value    int64            `json:"value"`
	Children []FlameGraphNode `json:"children"`
}

// ProfileAnalysis 是 AnalyzeProfile 返回的摘要。
type ProfileAnalysis struct {
	TotalSamples  int64             `json:"totalSamples"`
	TotalDuration time.Duration     `json:"totalDuration"`
	TopFunctions  []ProfileFunction `json:"topFunctions"`
	FlameGraph    *FlameGraphNode   `json:"flameGraph,omitempty"`
	// SampleUnit 为所选值列的单位（nanoseconds/count/bytes），便于前端展示。
	SampleUnit string `json:"sampleUnit"`
	// SampleType 为所选值列的类型名（samples/cpu/alloc_objects 等）。
	SampleType string `json:"sampleType"`
}

// analyzeTopN 控制返回的热点函数数量上限。
const analyzeTopN = 10

const maxProfileInputSize = int64(256 * 1024 * 1024)
const maxFlameGraphNodes = 10_000

var errProfileOutputLimit = errors.New("profile output exceeds 256 MiB limit")

// AnalyzeProfile 读取一个 pprof 二进制 profile 文件并返回热点函数摘要：
// 累计时间 / 自身时间 Top N、总采样数、总时长。
//
// 实现说明：解析 pprof protobuf 二进制格式（profile.proto）。由于构建
// 环境无法联网获取 github.com/google/pprof，这里使用标准库实现一个
// 最小化的 protobuf wire-format 解码器，仅提取分析所需的字段。
func (s *PProfService) AnalyzeProfile(profilePath string) (*ProfileAnalysis, error) {
	f, err := os.Open(profilePath)
	if err != nil {
		return nil, fmt.Errorf("open profile file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat profile file: %w", err)
	}
	if info.Size() > maxProfileInputSize {
		return nil, fmt.Errorf("profile input exceeds 256 MiB limit")
	}

	fileReader := io.LimitReader(f, maxProfileInputSize+1)
	header := make([]byte, 2)
	n, headerErr := io.ReadFull(fileReader, header)
	if headerErr != nil && !errors.Is(headerErr, io.EOF) && !errors.Is(headerErr, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("read profile header: %w", headerErr)
	}
	var profileReader io.Reader = io.MultiReader(bytes.NewReader(header[:n]), fileReader)
	var gr *gzip.Reader
	// runtime/pprof 写出的 profile 为 gzip 压缩的 protobuf（.pb.gz）。
	// 检测 gzip 魔数 0x1f 0x8b 并解压，兼容未压缩的裸 protobuf。
	if n == 2 && header[0] == 0x1f && header[1] == 0x8b {
		var gerr error
		gr, gerr = gzip.NewReader(profileReader)
		if gerr != nil {
			return nil, fmt.Errorf("open gzip: %w", gerr)
		}
		profileReader = gr
	}
	limited := &io.LimitedReader{R: profileReader, N: maxProfileInputSize + 1}
	rp, parseErr := parsePprof(limited)
	if gr != nil {
		if err := gr.Close(); err != nil && parseErr == nil {
			return nil, fmt.Errorf("close gzip: %w", err)
		}
	}
	if limited.N == 0 {
		return nil, fmt.Errorf("profile input exceeds 256 MiB limit")
	}
	if parseErr != nil {
		return nil, fmt.Errorf("parse profile: %w", parseErr)
	}
	return summarize(rp), nil
}

// AnalyzeTrace converts a runtime trace view to pprof using the Go toolchain,
// then reuses AnalyzeProfile so trace-derived data has the same flame graph UI.
func (s *PProfService) AnalyzeTrace(tracePath, view string) (*ProfileAnalysis, error) {
	switch view {
	case "net", "sync", "syscall", "sched":
	default:
		return nil, fmt.Errorf("unsupported trace profile view %q", view)
	}
	traceInput, err := copyTraceInput(tracePath)
	if err != nil {
		return nil, err
	}
	defer os.Remove(traceInput)
	goBin, err := exec.LookPath("go")
	if err != nil {
		return nil, fmt.Errorf("go toolchain is required to analyze traces")
	}
	tmp, err := os.CreateTemp("", "koyori-ide-trace-*.prof")
	if err != nil {
		return nil, fmt.Errorf("create trace profile temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("secure trace profile temp file: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := newTraceCommand(ctx, goBin, view, traceInput)
	output := &profileLimitWriter{writer: tmp, remaining: maxProfileInputSize + 1}
	stderr := &boundedBuffer{limit: 64 * 1024}
	cmd.Stdout = output
	cmd.Stderr = stderr
	runErr := cmd.Run()
	closeErr := tmp.Close()
	if output.exceeded {
		return nil, errProfileOutputLimit
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("trace analysis timed out: %w", ctx.Err())
	}
	if runErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return nil, fmt.Errorf("go tool trace: %w: %s", runErr, message)
		}
		return nil, fmt.Errorf("go tool trace: %w", runErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close trace profile temp file: %w", closeErr)
	}
	return s.AnalyzeProfile(tmpPath)
}

func copyTraceInput(tracePath string) (string, error) {
	info, err := os.Lstat(tracePath)
	if err != nil {
		return "", fmt.Errorf("stat trace file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("trace input must be a regular file")
	}
	if info.Size() > maxProfileInputSize {
		return "", fmt.Errorf("trace input exceeds 256 MiB limit")
	}

	source, err := os.Open(tracePath)
	if err != nil {
		return "", fmt.Errorf("open trace file: %w", err)
	}
	defer source.Close()
	openedInfo, err := source.Stat()
	if err != nil {
		return "", fmt.Errorf("stat opened trace file: %w", err)
	}
	if !openedInfo.Mode().IsRegular() {
		return "", fmt.Errorf("trace input must be a regular file")
	}

	tmp, err := os.CreateTemp("", "koyori-ide-trace-input-*.trace")
	if err != nil {
		return "", fmt.Errorf("create trace input temp file: %w", err)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("secure trace input temp file: %w", err)
	}
	written, copyErr := io.Copy(tmp, io.LimitReader(source, maxProfileInputSize+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		return "", fmt.Errorf("copy trace input: %w", copyErr)
	}
	if written > maxProfileInputSize {
		return "", fmt.Errorf("trace input exceeds 256 MiB limit")
	}
	if closeErr != nil {
		return "", fmt.Errorf("close trace input temp file: %w", closeErr)
	}
	removeTemp = false
	return tmpPath, nil
}

func newTraceCommand(ctx context.Context, goBin, view, tracePath string) *exec.Cmd {
	cmd := commandContext(ctx, goBin, "tool", "trace", "-pprof="+view, tracePath)
	configureCoverageProcessTree(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return terminateCoverageProcessTree(cmd.Process)
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

type profileLimitWriter struct {
	writer    io.Writer
	remaining int64
	exceeded  bool
}

func (w *profileLimitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		w.exceeded = true
		if w.remaining <= 0 {
			return 0, errProfileOutputLimit
		}
		n, err := w.writer.Write(p[:w.remaining])
		w.remaining -= int64(n)
		if err != nil {
			return n, err
		}
		return n, errProfileOutputLimit
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	originalLen := len(p)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return originalLen, nil
}

// summarize 将解析后的原始 profile 聚合成 ProfileAnalysis。
func summarize(rp *rawProfile) *ProfileAnalysis {
	out := &ProfileAnalysis{
		TotalSamples: int64(len(rp.samples)),
		SampleUnit:   "",
		SampleType:   "",
	}
	if len(rp.samples) == 0 {
		return out
	}

	// 选择值列：优先 unit == "nanoseconds"，否则取最后一列。
	chosen := 0
	for i := len(rp.sampleTypes) - 1; i >= 0; i-- {
		if rp.stringAt(rp.sampleTypes[i].unitIdx) == "nanoseconds" {
			chosen = i
			break
		}
	}
	if chosen >= len(rp.sampleTypes) {
		chosen = 0
	}
	if chosen < len(rp.sampleTypes) {
		out.SampleType = rp.stringAt(rp.sampleTypes[chosen].typeIdx)
		out.SampleUnit = rp.stringAt(rp.sampleTypes[chosen].unitIdx)
	}
	out.FlameGraph = buildFlameGraph(rp, chosen)

	// 累计每个 sample 的总值用于百分比与时长换算。
	var totalValue int64
	cumByFn := make(map[uint64]int64)
	flatByFn := make(map[uint64]int64)
	for _, sm := range rp.samples {
		if chosen >= len(sm.values) {
			continue
		}
		v := sm.values[chosen]
		totalValue += v
		// cumulative：栈中所有函数累加。
		seenFunctions := make(map[uint64]struct{})
		for _, locID := range sm.locationIDs {
			loc, ok := rp.locations[locID]
			if !ok {
				continue
			}
			for _, ln := range loc.lines {
				if _, seen := seenFunctions[ln.functionID]; seen {
					continue
				}
				seenFunctions[ln.functionID] = struct{}{}
				cumByFn[ln.functionID] += v
			}
		}
		// flat：叶子帧（locationIDs[0]）的函数。
		if len(sm.locationIDs) > 0 {
			if leaf, ok := rp.locations[sm.locationIDs[0]]; ok {
				if len(leaf.lines) > 0 {
					flatByFn[leaf.lines[0].functionID] += v
				}
			}
		}
	}

	// 总时长：nanoseconds 列直接取总值；否则用 duration_nanos；再否则取总值。
	switch {
	case out.SampleUnit == "nanoseconds":
		out.TotalDuration = time.Duration(totalValue)
	case rp.durationNanos > 0:
		out.TotalDuration = time.Duration(rp.durationNanos)
	default:
		out.TotalDuration = time.Duration(totalValue)
	}

	// 把原始值换算为 time.Duration。
	toDuration := func(raw int64) time.Duration {
		switch {
		case out.SampleUnit == "nanoseconds":
			return time.Duration(raw)
		case out.SampleUnit == "count" && rp.durationNanos > 0 && totalValue > 0:
			// CPU profile：把采样计数按比例换算为时长。
			return time.Duration(raw * rp.durationNanos / totalValue)
		default:
			// bytes 等非时间单位：保留量级，前端按 Unit 展示。
			return time.Duration(raw)
		}
	}

	funcs := make([]ProfileFunction, 0, len(cumByFn))
	for fnID, cum := range cumByFn {
		fnName := rp.functionName(fnID)
		flat := flatByFn[fnID]
		pf := ProfileFunction{
			Name:           fnName,
			CumulativeTime: toDuration(cum),
			FlatTime:       toDuration(flat),
		}
		if totalValue > 0 {
			pf.CumulativePercent = float64(cum) / float64(totalValue) * 100
			pf.FlatPercent = float64(flat) / float64(totalValue) * 100
		}
		funcs = append(funcs, pf)
	}
	// 按累计时间降序，平局按名称保证确定性。
	sort.Slice(funcs, func(i, j int) bool {
		if funcs[i].CumulativeTime != funcs[j].CumulativeTime {
			return funcs[i].CumulativeTime > funcs[j].CumulativeTime
		}
		return funcs[i].Name < funcs[j].Name
	})
	if len(funcs) > analyzeTopN {
		funcs = funcs[:analyzeTopN]
	}
	out.TopFunctions = funcs
	return out
}

type flameGraphBuilder struct {
	name     string
	value    int64
	children map[string]*flameGraphBuilder
}

const truncatedFlameFrame = "[truncated]"
const truncatedFlameKey = "\x00koyori-ide-truncated"

func buildFlameGraph(rp *rawProfile, chosen int) *FlameGraphNode {
	root := &flameGraphBuilder{name: "all", children: map[string]*flameGraphBuilder{}}
	nodeCount := 1
	for _, sample := range rp.samples {
		if chosen >= len(sample.values) || sample.values[chosen] <= 0 {
			continue
		}
		value := sample.values[chosen]
		root.value += value
		stack := make([]string, 0, len(sample.locationIDs))
		for i := len(sample.locationIDs) - 1; i >= 0; i-- {
			location, ok := rp.locations[sample.locationIDs[i]]
			if !ok {
				continue
			}
			for lineIndex := len(location.lines) - 1; lineIndex >= 0; lineIndex-- {
				name := rp.functionName(location.lines[lineIndex].functionID)
				if name != "" {
					stack = append(stack, name)
				}
			}
		}
		current := root
		missing := 0
		for _, name := range stack {
			if missing > 0 {
				missing++
				continue
			}
			child := current.children[name]
			if child == nil {
				missing = 1
				continue
			}
			current = child
		}
		// Keep one node in reserve for an explicit overflow bucket. Routing
		// complete samples there preserves value conservation at the root.
		if len(stack) == 0 || (missing > 0 && nodeCount+missing > maxFlameGraphNodes-1) {
			truncated := root.children[truncatedFlameKey]
			if truncated == nil {
				truncated = &flameGraphBuilder{name: truncatedFlameFrame, children: map[string]*flameGraphBuilder{}}
				root.children[truncatedFlameKey] = truncated
				nodeCount++
			}
			truncated.value += value
			continue
		}
		current = root
		for _, name := range stack {
			child := current.children[name]
			if child == nil {
				child = &flameGraphBuilder{name: name, children: map[string]*flameGraphBuilder{}}
				current.children[name] = child
				nodeCount++
			}
			child.value += value
			current = child
		}
	}
	if root.value == 0 {
		return nil
	}
	return finalizeFlameGraph(root, "0")
}

func finalizeFlameGraph(builder *flameGraphBuilder, id string) *FlameGraphNode {
	children := make([]*flameGraphBuilder, 0, len(builder.children))
	for _, child := range builder.children {
		children = append(children, child)
	}
	sort.Slice(children, func(i, j int) bool {
		if children[i].value != children[j].value {
			return children[i].value > children[j].value
		}
		return children[i].name < children[j].name
	})
	node := &FlameGraphNode{ID: id, Name: builder.name, Value: builder.value, Children: make([]FlameGraphNode, 0, len(children))}
	for index, child := range children {
		node.Children = append(node.Children, *finalizeFlameGraph(child, fmt.Sprintf("%s.%d", id, index)))
	}
	return node
}

// ---------------- 最小化 pprof protobuf 解码器 ----------------
// 仅依赖 encoding/binary，避免引入外部依赖。profile.proto 字段定义：
//   Profile        { sample_type=1 Sample; sample=2 Sample; mapping=3;
//                    location=4 Location; function=5 Function;
//                    string_table=6 string; time_nanos=9 int64;
//                    duration_nanos=10 int64; period_type=11 ValueType;
//                    period=12 int64 }
//   ValueType      { type=1 int64; unit=2 int64 }
//   Sample         { location_id=1 uint64(packed); value=2 int64(packed) }
//   Location       { id=1 uint64; line=4 Line }
//   Line           { function_id=1 uint64; line=2 int64 }
//   Function       { id=1 uint64; name=2 int64; system_name=3;
//                    filename=4; start_line=5 }

type rawValueType struct {
	typeIdx int64
	unitIdx int64
}

type rawLine struct {
	functionID uint64
	line       int64
}

type rawLocation struct {
	id    uint64
	lines []rawLine
}

type rawFunction struct {
	id      uint64
	nameIdx int64
}

type rawSample struct {
	locationIDs []uint64
	values      []int64
}

type rawProfile struct {
	sampleTypes   []rawValueType
	samples       []rawSample
	locations     map[uint64]*rawLocation
	functions     map[uint64]*rawFunction
	strings       []string
	timeNanos     int64
	durationNanos int64
}

func (rp *rawProfile) stringAt(idx int64) string {
	if idx < 0 || int(idx) >= len(rp.strings) {
		return ""
	}
	return rp.strings[idx]
}

func (rp *rawProfile) functionName(id uint64) string {
	if id == 0 {
		return ""
	}
	fn, ok := rp.functions[id]
	if !ok {
		return ""
	}
	name := rp.stringAt(fn.nameIdx)
	if name == "" {
		return fmt.Sprintf("0x%x", id)
	}
	return name
}

func parsePprof(r io.Reader) (*rawProfile, error) {
	rp := &rawProfile{
		locations: make(map[uint64]*rawLocation),
		functions: make(map[uint64]*rawFunction),
	}
	err := iterReaderFields(r, func(num, wire int, v uint64, sub []byte) error {
		switch num {
		case 1: // sample_type (ValueType)
			if wire != 2 {
				return nil
			}
			rp.sampleTypes = append(rp.sampleTypes, parseValueType(sub))
		case 2: // sample (Sample)
			if wire != 2 {
				return nil
			}
			sm, err := parseSample(sub)
			if err != nil {
				return err
			}
			rp.samples = append(rp.samples, sm)
		case 4: // location (Location)
			if wire != 2 {
				return nil
			}
			loc := parseLocation(sub)
			if loc != nil && loc.id != 0 {
				rp.locations[loc.id] = loc
			}
		case 5: // function (Function)
			if wire != 2 {
				return nil
			}
			fn := parseFunction(sub)
			if fn != nil && fn.id != 0 {
				rp.functions[fn.id] = fn
			}
		case 6: // string_table (string)
			if wire != 2 {
				return nil
			}
			rp.strings = append(rp.strings, string(sub))
		case 9: // time_nanos
			if wire == 0 {
				rp.timeNanos = int64(v)
			}
		case 10: // duration_nanos
			if wire == 0 {
				rp.durationNanos = int64(v)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rp, nil
}

func iterReaderFields(r io.Reader, fn func(num, wire int, v uint64, sub []byte) error) error {
	br := bufio.NewReader(r)
	for {
		tag, err := binary.ReadUvarint(br)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		num := int(tag >> 3)
		wire := int(tag & 7)
		switch wire {
		case 0:
			v, err := binary.ReadUvarint(br)
			if err != nil {
				return err
			}
			if err := fn(num, wire, v, nil); err != nil {
				return err
			}
		case 1:
			sub := make([]byte, 8)
			if _, err := io.ReadFull(br, sub); err != nil {
				return errors.New("protobuf: short fixed64")
			}
			if err := fn(num, wire, binary.LittleEndian.Uint64(sub), sub); err != nil {
				return err
			}
		case 2:
			ln, err := binary.ReadUvarint(br)
			if err != nil {
				return err
			}
			if ln > uint64(maxProfileInputSize) {
				return fmt.Errorf("protobuf: length-delimited field exceeds 256 MiB limit")
			}
			sub := make([]byte, int(ln))
			if _, err := io.ReadFull(br, sub); err != nil {
				return errors.New("protobuf: short length-delimited")
			}
			if err := fn(num, wire, 0, sub); err != nil {
				return err
			}
		case 5:
			sub := make([]byte, 4)
			if _, err := io.ReadFull(br, sub); err != nil {
				return errors.New("protobuf: short fixed32")
			}
			if err := fn(num, wire, uint64(binary.LittleEndian.Uint32(sub)), sub); err != nil {
				return err
			}
		default:
			return fmt.Errorf("protobuf: unknown wire type %d", wire)
		}
	}
}

func parseValueType(b []byte) rawValueType {
	var vt rawValueType
	_ = iterFields(b, func(num, wire int, v uint64, sub []byte) error {
		switch num {
		case 1:
			if wire == 0 {
				vt.typeIdx = int64(v)
			}
		case 2:
			if wire == 0 {
				vt.unitIdx = int64(v)
			}
		}
		return nil
	})
	return vt
}

func parseSample(b []byte) (rawSample, error) {
	var sm rawSample
	err := iterFields(b, func(num, wire int, v uint64, sub []byte) error {
		switch num {
		case 1: // location_id (repeated uint64, packed)
			if wire == 2 {
				sm.locationIDs = append(sm.locationIDs, readPackedUints(sub)...)
			} else if wire == 0 {
				sm.locationIDs = append(sm.locationIDs, v)
			}
		case 2: // value (repeated int64, packed)
			if wire == 2 {
				for _, u := range readPackedUints(sub) {
					sm.values = append(sm.values, int64(u))
				}
			} else if wire == 0 {
				sm.values = append(sm.values, int64(v))
			}
		}
		return nil
	})
	return sm, err
}

func parseLocation(b []byte) *rawLocation {
	loc := &rawLocation{}
	_ = iterFields(b, func(num, wire int, v uint64, sub []byte) error {
		switch num {
		case 1: // id
			if wire == 0 {
				loc.id = v
			}
		case 4: // line (repeated Line)
			if wire == 2 {
				loc.lines = append(loc.lines, parseLine(sub))
			}
		}
		return nil
	})
	return loc
}

func parseLine(b []byte) rawLine {
	var ln rawLine
	_ = iterFields(b, func(num, wire int, v uint64, sub []byte) error {
		switch num {
		case 1: // function_id
			if wire == 0 {
				ln.functionID = v
			}
		case 2: // line
			if wire == 0 {
				ln.line = int64(v)
			}
		}
		return nil
	})
	return ln
}

func parseFunction(b []byte) *rawFunction {
	fn := &rawFunction{}
	_ = iterFields(b, func(num, wire int, v uint64, sub []byte) error {
		switch num {
		case 1: // id
			if wire == 0 {
				fn.id = v
			}
		case 2: // name
			if wire == 0 {
				fn.nameIdx = int64(v)
			}
		}
		return nil
	})
	return fn
}

// readPackedUints 解码 packed repeated varint 字段的内容。
func readPackedUints(b []byte) []uint64 {
	var out []uint64
	for len(b) > 0 {
		x, n, err := readVarint(b)
		if err != nil {
			return out
		}
		out = append(out, x)
		b = b[n:]
	}
	return out
}

// iterFields 遍历 protobuf 消息的每个字段，回调 (fieldNum, wireType, varintVal, lengthDelimitedData)。
// wire==0 时 v 为 varint 值；wire==2 时 sub 为长度前缀内容；wire==1/5 时 sub 为定长字节。
func iterFields(b []byte, fn func(num, wire int, v uint64, sub []byte) error) error {
	for len(b) > 0 {
		tag, n, err := readVarint(b)
		if err != nil {
			return err
		}
		b = b[n:]
		num := int(tag >> 3)
		wire := int(tag & 7)
		switch wire {
		case 0: // varint
			v, n, err := readVarint(b)
			if err != nil {
				return err
			}
			b = b[n:]
			if err := fn(num, wire, v, nil); err != nil {
				return err
			}
		case 1: // fixed64
			if len(b) < 8 {
				return errors.New("protobuf: short fixed64")
			}
			v := binary.LittleEndian.Uint64(b)
			sub := b[:8]
			b = b[8:]
			if err := fn(num, wire, v, sub); err != nil {
				return err
			}
		case 2: // length-delimited
			ln, n, err := readVarint(b)
			if err != nil {
				return err
			}
			b = b[n:]
			if uint64(len(b)) < ln {
				return errors.New("protobuf: short length-delimited")
			}
			sub := b[:ln]
			b = b[ln:]
			if err := fn(num, wire, 0, sub); err != nil {
				return err
			}
		case 5: // fixed32
			if len(b) < 4 {
				return errors.New("protobuf: short fixed32")
			}
			v := uint64(binary.LittleEndian.Uint32(b))
			sub := b[:4]
			b = b[4:]
			if err := fn(num, wire, v, sub); err != nil {
				return err
			}
		default:
			return fmt.Errorf("protobuf: unknown wire type %d", wire)
		}
	}
	return nil
}

// readVarint 解码 base-128 varint，返回 (值, 消耗字节数, 错误)。
func readVarint(b []byte) (uint64, int, error) {
	var x uint64
	var s uint
	for i, c := range b {
		if i >= 10 {
			return 0, 0, errors.New("varint too long")
		}
		if c < 0x80 {
			if i == 9 && c > 1 {
				return 0, 0, errors.New("varint overflow")
			}
			return x | uint64(c)<<s, i + 1, nil
		}
		x |= uint64(c&0x7f) << s
		s += 7
	}
	return 0, 0, errors.New("varint truncated")
}

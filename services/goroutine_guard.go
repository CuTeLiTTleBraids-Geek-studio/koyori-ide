package services

// P20 P1-05: top-level recover hooks for long-lived goroutines.
//
// 不做无差别撒网：只有三类长生命周期 goroutine 加顶层 recover ——
// 事件泵（main 时间泵、terminal PTY 输出泵）、流 worker（AI stream worker
// 及其 idle 泵、MCP dispatcher / SSE readLoop、LSP readLoop）、服务后台
// 循环（secrets 审计写入泵）。单点 panic 不再击穿整个桌面进程：panic 值
// 转为结构化错误上报（slog + 可插拔 sink，bootstrap 挂接 CrashService），
// goroutine 走完自身既有 defer 清理后退出（fail-closed：调用方按
// goroutine 已消失处理，不得依赖其继续产出）。短生命周期、有 defer 包裹
// 语义的 reaper / fire-and-forget goroutine 保持原样。

import (
	"log/slog"
	"runtime"
	"sync"
)

// GoroutinePanicSink receives one report per recovered goroutine panic.
// scope names the guarded site (e.g. "ai:stream-worker"); stack is the
// panicking goroutine's stack trace.
type GoroutinePanicSink func(scope string, panicValue any, stack []byte)

var (
	goroutinePanicSinkMu sync.Mutex
	goroutinePanicSink   GoroutinePanicSink
)

// SetGoroutinePanicSink swaps the panic report sink (bootstrap wires this to
// CrashService; tests swap in a capturing sink). nil restores the default
// slog-only sink. Returns the previous sink.
func SetGoroutinePanicSink(sink GoroutinePanicSink) (previous GoroutinePanicSink) {
	goroutinePanicSinkMu.Lock()
	defer goroutinePanicSinkMu.Unlock()
	previous = goroutinePanicSink
	goroutinePanicSink = sink
	return previous
}

// RecoverGoroutinePanic must be deferred at the top of a guarded long-lived
// goroutine: `defer RecoverGoroutinePanic("ai:stream-worker")`. It converts a
// panic into a structured error report instead of letting the unwind kill the
// process; other deferred cleanups in the goroutine still run.
func RecoverGoroutinePanic(scope string) {
	r := recover()
	if r == nil {
		return
	}
	stack := make([]byte, 16*1024)
	n := runtime.Stack(stack, false)
	report := stack[:n]
	slog.Error("goroutine panic recovered (fail-closed)",
		"scope", scope, "panic", r, "stack", string(report))
	goroutinePanicSinkMu.Lock()
	sink := goroutinePanicSink
	goroutinePanicSinkMu.Unlock()
	if sink != nil {
		sink(scope, r, report)
	}
}

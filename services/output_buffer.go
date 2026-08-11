package services

import (
	"bytes"
	"sync"
	"time"
)

// outputBufferMaxBytes is the default cap on the buffered output. When
// exceeded, the oldest data is dropped to make room for new data. This
// bounds memory use when the frontend isn't draining the buffer quickly
// enough (e.g. a long `npm install` running in a backgrounded terminal).
//
// 1 MiB is large enough to keep recent scrollback visible after a Read,
// but small enough that a runaway process can't exhaust the heap.
//
// G-PERF-03: the 1 MiB cap is the performance/memory gate for terminal
// output. It must not be raised without revisiting the frontend xterm
// scrollback limit (see TerminalPanel.vue scrollback: 5000) — together
// they bound peak memory per terminal session.
const outputBufferMaxBytes = 1 << 20

const (
	terminalOutputBatchInterval = 16 * time.Millisecond
	terminalOutputBatchMaxBytes = 4 << 10
)

type outputBuffer struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	notify   chan struct{}
	maxBytes int
}

func newOutputBuffer() *outputBuffer {
	return &outputBuffer{
		notify:   make(chan struct{}, 1),
		maxBytes: outputBufferMaxBytes,
	}
}

// Append writes data to the buffer. If the buffer would exceed maxBytes,
// the oldest bytes are dropped first so that the most recent output is
// always retained (N-66). Dropping oldest matches terminal semantics:
// users care about what just happened, not what scrolled off the top.
func (o *outputBuffer) Append(data []byte) {
	o.mu.Lock()
	o.buf.Write(data)
	// N-66: enforce the cap. Trimming after write keeps the code simple
	// and avoids underflow when the incoming chunk itself exceeds the cap
	// (in which case we keep only the tail of the new data).
	if max := o.maxBytes; max > 0 && o.buf.Len() > max {
		excess := o.buf.Len() - max
		// Next(n) reads and discards the first n bytes from the buffer.
		_ = o.buf.Next(excess)
	}
	o.mu.Unlock()
	select {
	case o.notify <- struct{}{}:
	default:
	}
}

func (o *outputBuffer) Read(timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		o.mu.Lock()
		hasData := o.buf.Len() > 0
		o.mu.Unlock()
		if hasData {
			break
		}
		select {
		case <-o.notify:
		case <-timer.C:
			return o.drain()
		}
	}

	coalesceFor := time.Until(deadline)
	if coalesceFor > 50*time.Millisecond {
		coalesceFor = 50 * time.Millisecond
	}
	if coalesceFor > 0 {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(coalesceFor)
		for {
			select {
			case <-o.notify:
			case <-timer.C:
				return o.drain()
			}
		}
	}
	return o.drain()
}

func (o *outputBuffer) drain() string {
	o.mu.Lock()
	s := o.buf.String()
	o.buf.Reset()
	o.mu.Unlock()
	return s
}

// outputBatcher coalesces terminal output before it is emitted to the
// frontend. Its goroutine owns all batching state, so event emission never
// happens while holding a lock. Close flushes pending output and waits for
// the goroutine to exit.
type outputBatcher struct {
	input     chan string
	stop      chan struct{}
	done      chan struct{}
	emit      func(string)
	closeOnce sync.Once
}

func newOutputBatcher(emit func(string)) *outputBatcher {
	b := &outputBatcher{
		input: make(chan string),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
		emit:  emit,
	}
	go b.run()
	return b
}

func (b *outputBatcher) Append(data []byte) {
	if len(data) == 0 {
		return
	}
	select {
	case b.input <- string(data):
	case <-b.stop:
	}
}

func (b *outputBatcher) Close() {
	b.closeOnce.Do(func() {
		close(b.stop)
	})
	<-b.done
}

func (b *outputBatcher) run() {
	defer close(b.done)

	timer := time.NewTimer(terminalOutputBatchInterval)
	if !timer.Stop() {
		<-timer.C
	}
	var timerC <-chan time.Time
	var buf bytes.Buffer

	stopTimer := func() {
		if timerC == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timerC = nil
	}

	flush := func() {
		if buf.Len() == 0 {
			return
		}
		data := buf.String()
		buf.Reset()
		if b.emit != nil {
			b.emit(data)
		}
	}

	appendData := func(data string) {
		if buf.Len() == 0 {
			timer.Reset(terminalOutputBatchInterval)
			timerC = timer.C
		}
		buf.WriteString(data)
		if buf.Len() >= terminalOutputBatchMaxBytes {
			stopTimer()
			flush()
		}
	}

	for {
		select {
		case data := <-b.input:
			appendData(data)
		case <-timerC:
			timerC = nil
			flush()
		case <-b.stop:
			stopTimer()
			// Drain any send that was already racing with Close so accepted
			// output is included in the final flush.
			for {
				select {
				case data := <-b.input:
					buf.WriteString(data)
				default:
					flush()
					return
				}
			}
		}
	}
}

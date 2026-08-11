package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	DebugThreadsUpdatedEventName = "debug:threads-updated"
	DebugThreadSelectedEventName = "debug:thread-selected"
	DebugThreadStoppedEventName  = "debug:thread-stopped"

	ThreadStateRunning  = "running"
	ThreadStateStopped  = "stopped"
	ThreadStateStepping = "stepping"

	debugThreadStackPageSize  = 64
	debugThreadMaxStackFrames = 16384
)

var (
	ErrDebugThreadsStaleRun   = errors.New("debug thread run changed")
	ErrDebugThreadsStaleState = errors.New("debug thread state changed")
)

// StackFrame preserves the complete DAP frame contract used by the threads UI.
// File mirrors Source for compatibility with the existing debugger store.
type StackFrame struct {
	ID               int    `json:"id"`
	Name             string `json:"name"`
	Source           string `json:"source"`
	File             string `json:"file,omitempty"`
	Line             int    `json:"line"`
	Column           int    `json:"column"`
	EndLine          int    `json:"endLine,omitempty"`
	EndColumn        int    `json:"endColumn,omitempty"`
	Module           string `json:"module,omitempty"`
	PresentationHint string `json:"presentationHint,omitempty"`
	AsyncBoundary    bool   `json:"asyncBoundary,omitempty"`
}

// ThreadInfo is the frontend-facing state for one DAP thread.
type ThreadInfo struct {
	ID       int          `json:"id"`
	Name     string       `json:"name"`
	State    string       `json:"state"`
	Frames   []StackFrame `json:"frames,omitempty"`
	Selected bool         `json:"selected"`
}

// DebugThreadsCapabilities contains the DAP capabilities used by this module.
type DebugThreadsCapabilities struct {
	SupportsStepInTargets                 bool `json:"supportsStepInTargets"`
	SupportsStepInTargetsRequest          bool `json:"supportsStepInTargetsRequest"`
	SupportsTerminateRequest              bool `json:"supportsTerminateRequest"`
	SupportsSingleThreadExecutionRequests bool `json:"supportsSingleThreadExecutionRequests"`
}

// DebugThreadsRunIdentity is an opaque, owner-bound run identity supplied by
// the backend adapter. RunID must change when the underlying session owner,
// connection, or run changes, even if Generation is reused.
type DebugThreadsRunIdentity struct {
	SessionID  string `json:"sessionId"`
	RunID      string `json:"runId"`
	Generation uint64 `json:"generation"`
}

// DebugThreadsRunToken is captured once and forwarded with DAP events.
type DebugThreadsRunToken = DebugThreadsRunIdentity

// DebugThreadsSessionSnapshot is an atomic backend view of one debug run.
// StateRevision must change whenever execution state changes.
type DebugThreadsSessionSnapshot struct {
	SessionID     string `json:"sessionId"`
	RunID         string `json:"runId"`
	Generation    uint64 `json:"generation"`
	StateRevision uint64 `json:"stateRevision"`
	Stopped       bool   `json:"stopped"`
	ThreadID      int    `json:"threadId"`
	StopReason    string `json:"stopReason,omitempty"`
}

// Identity returns the stale-run guard used for backend requests.
func (s DebugThreadsSessionSnapshot) Identity() DebugThreadsRunIdentity {
	return DebugThreadsRunIdentity{
		SessionID:  s.SessionID,
		RunID:      s.RunID,
		Generation: s.Generation,
	}
}

// DebugThreadsSessionUpdate describes an atomic update to the canonical debug
// state. ApplySessionUpdate must reject a stale snapshot or run identity.
type DebugThreadsSessionUpdate struct {
	ThreadID        *int              `json:"threadId,omitempty"`
	Stopped         *bool             `json:"stopped,omitempty"`
	StopReason      *string           `json:"stopReason,omitempty"`
	Stack           []DebugStackFrame `json:"stack,omitempty"`
	ReplaceStack    bool              `json:"replaceStack,omitempty"`
	StackTotal      int               `json:"stackTotal,omitempty"`
	StackHasMore    bool              `json:"stackHasMore,omitempty"`
	ClearLocals     bool              `json:"clearLocals,omitempty"`
	ClearAsyncStack bool              `json:"clearAsyncStack,omitempty"`
	Touch           bool              `json:"touch,omitempty"`
}

// DebugThreadsBackend is the public integration boundary. Implementations
// must be concurrency-safe. Request must reject a stale run identity, and
// ApplySessionUpdate must atomically reject stale run or StateRevision values.
type DebugThreadsBackend interface {
	Snapshot(sessionID string) (DebugThreadsSessionSnapshot, error)
	Request(run DebugThreadsRunIdentity, command string, args map[string]any) (json.RawMessage, error)
	SetActiveSession(sessionID string) error
	ApplySessionUpdate(expected DebugThreadsSessionSnapshot, update DebugThreadsSessionUpdate) error
}

// DebugStoppedEvent is the DAP stopped-event subset used by this module.
type DebugStoppedEvent struct {
	Reason            string `json:"reason,omitempty"`
	ThreadID          int    `json:"threadId,omitempty"`
	AllThreadsStopped bool   `json:"allThreadsStopped"`
}

// DebugContinuedEvent preserves the optional DAP field. Per DAP, omitted and
// true both mean all threads continued; only an explicit false means one thread.
type DebugContinuedEvent struct {
	ThreadID            int   `json:"threadId,omitempty"`
	AllThreadsContinued *bool `json:"allThreadsContinued,omitempty"`
}

// DebugThreadsUpdatedEvent is emitted after cached thread state changes.
type DebugThreadsUpdatedEvent struct {
	SessionID         string       `json:"sessionId"`
	Threads           []ThreadInfo `json:"threads"`
	AllThreadsStopped bool         `json:"allThreadsStopped"`
}

// DebugThreadSelectedEvent is emitted when the active thread changes.
type DebugThreadSelectedEvent struct {
	SessionID string `json:"sessionId"`
	ThreadID  int    `json:"threadId"`
}

// DebugThreadStoppedEvent is emitted for every DAP stopped event so consumers
// can react without diffing the complete thread snapshot.
type DebugThreadStoppedEvent struct {
	SessionID         string `json:"sessionId"`
	ThreadID          int    `json:"threadId"`
	Reason            string `json:"reason,omitempty"`
	AllThreadsStopped bool   `json:"allThreadsStopped"`
}

type DebugThreadsEventEmitter func(name string, payload any)

type debugThreadsRunBinding struct {
	identity DebugThreadsRunIdentity
	revision uint64
}

type debugThreadStackPage struct {
	levels      int
	frames      []StackFrame
	totalFrames int
}

// DebugThreadsService implements multi-thread DAP behavior through a public
// adapter, so it can be registered without depending on a concrete debugger.
type DebugThreadsService struct {
	backend DebugThreadsBackend

	opMu sync.Mutex
	mu   sync.RWMutex

	threads           map[string]map[int]*ThreadInfo
	order             map[string][]int
	selected          map[string]int
	allThreadsStopped map[string]bool
	capabilities      map[string]DebugThreadsCapabilities
	capabilitiesKnown map[string]bool
	pendingStackPages map[string]map[int]map[int]debugThreadStackPage
	runs              map[string]debugThreadsRunBinding
	app               *application.App
	emit              DebugThreadsEventEmitter
}

func NewDebugThreadsService(backend DebugThreadsBackend) *DebugThreadsService {
	return newDebugThreadsService(backend, nil)
}

func NewDebugThreadsServiceWithEmitter(backend DebugThreadsBackend, emit DebugThreadsEventEmitter) *DebugThreadsService {
	return newDebugThreadsService(backend, emit)
}

func newDebugThreadsService(backend DebugThreadsBackend, emit DebugThreadsEventEmitter) *DebugThreadsService {
	return &DebugThreadsService{
		backend:           backend,
		threads:           make(map[string]map[int]*ThreadInfo),
		order:             make(map[string][]int),
		selected:          make(map[string]int),
		allThreadsStopped: make(map[string]bool),
		capabilities:      make(map[string]DebugThreadsCapabilities),
		capabilitiesKnown: make(map[string]bool),
		pendingStackPages: make(map[string]map[int]map[int]debugThreadStackPage),
		runs:              make(map[string]debugThreadsRunBinding),
		emit:              emit,
	}
}

func (s *DebugThreadsService) SetApp(app *application.App) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.app = app
	s.mu.Unlock()
}

func (s *DebugThreadsService) ListThreads(ctx context.Context, sessionID string) ([]ThreadInfo, error) {
	if err := debugThreadsContextError(ctx); err != nil {
		return nil, err
	}
	snapshot, revision, err := s.captureSnapshot(sessionID)
	if contextErr := debugThreadsContextError(ctx); contextErr != nil {
		return nil, contextErr
	}
	if err != nil {
		return nil, err
	}
	body, requestErr := s.backend.Request(snapshot.Identity(), "threads", map[string]any{})
	if err := debugThreadsContextError(ctx); err != nil {
		return nil, err
	}
	if requestErr != nil {
		return nil, fmt.Errorf("list threads for session %q: %w", snapshot.SessionID, requestErr)
	}
	var response struct {
		Threads []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"threads"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("parse threads response: %w", err)
	}
	seen := make(map[int]struct{}, len(response.Threads))
	for _, thread := range response.Threads {
		if thread.ID <= 0 {
			return nil, fmt.Errorf("threads response contains invalid thread id %d", thread.ID)
		}
		if _, exists := seen[thread.ID]; exists {
			return nil, fmt.Errorf("threads response contains duplicate thread id %d", thread.ID)
		}
		seen[thread.ID] = struct{}{}
	}

	var result []ThreadInfo
	applied, err := s.commitResponse(snapshot, revision, nil, func() {
		previous := cloneThreadMap(s.threads[snapshot.SessionID])
		previousSelected := s.selected[snapshot.SessionID]
		previousAllStopped := s.allThreadsStopped[snapshot.SessionID]
		cacheInvalidated := false
		selected := previousSelected
		if selected == 0 {
			selected = snapshot.ThreadID
		}
		if selected == 0 && len(response.Threads) > 0 {
			selected = response.Threads[0].ID
		}
		allStopped := s.allThreadsStopped[snapshot.SessionID]
		if !snapshot.Stopped {
			allStopped = false
		}
		next := make(map[int]*ThreadInfo, len(response.Threads))
		order := make([]int, 0, len(response.Threads))
		for _, thread := range response.Threads {
			name := strings.TrimSpace(thread.Name)
			info := &ThreadInfo{ID: thread.ID, Name: name, State: ThreadStateRunning}
			if old := previous[thread.ID]; old != nil {
				info.State = normalizeThreadState(old.State)
				info.Frames = cloneStackFrames(old.Frames)
			}
			if snapshot.Stopped && (allStopped || thread.ID == selected) {
				info.State = ThreadStateStopped
			} else if !snapshot.Stopped {
				if info.State == ThreadStateStopped {
					info.State = ThreadStateRunning
				}
				info.Frames = nil
			}
			if old := previous[thread.ID]; old != nil &&
				(normalizeThreadState(old.State) != info.State ||
					(len(old.Frames) > 0 && len(info.Frames) == 0)) {
				cacheInvalidated = true
			}
			info.Selected = thread.ID == selected
			next[thread.ID] = info
			order = append(order, thread.ID)
		}
		if _, exists := next[selected]; !exists {
			selected = 0
			if len(order) > 0 {
				selected = order[0]
				next[selected].Selected = true
			}
		}
		if !snapshot.Stopped {
			if len(s.pendingStackPages[snapshot.SessionID]) > 0 {
				cacheInvalidated = true
			}
			delete(s.pendingStackPages, snapshot.SessionID)
		} else if pending := s.pendingStackPages[snapshot.SessionID]; pending != nil {
			for threadID := range pending {
				if next[threadID] == nil {
					delete(pending, threadID)
				}
			}
			if len(pending) == 0 {
				delete(s.pendingStackPages, snapshot.SessionID)
			}
		}
		s.threads[snapshot.SessionID] = next
		s.order[snapshot.SessionID] = order
		s.selected[snapshot.SessionID] = selected
		s.allThreadsStopped[snapshot.SessionID] = allStopped
		topologyChanged := previousSelected != selected ||
			previousAllStopped != allStopped || len(previous) != len(next)
		if !topologyChanged {
			for threadID := range previous {
				if next[threadID] == nil {
					topologyChanged = true
					break
				}
			}
		}
		if topologyChanged || cacheInvalidated {
			s.bumpRevisionLocked(snapshot.SessionID)
		}
		result = s.cachedThreadsLocked(snapshot.SessionID)
	})
	if err != nil {
		return nil, err
	}
	if !applied {
		return s.cachedThreads(snapshot.SessionID), nil
	}
	s.emitThreadsUpdated(snapshot.SessionID)
	return result, nil
}

func (s *DebugThreadsService) GetThreadStackTrace(
	ctx context.Context,
	sessionID string,
	threadID, startFrame, levels int,
) ([]StackFrame, error) {
	if err := debugThreadsContextError(ctx); err != nil {
		return nil, err
	}
	if threadID <= 0 {
		return nil, fmt.Errorf("thread id must be positive")
	}
	if startFrame < 0 {
		return nil, fmt.Errorf("start frame must not be negative")
	}
	if levels < 0 {
		return nil, fmt.Errorf("levels must not be negative")
	}
	snapshot, revision, err := s.captureSnapshot(sessionID)
	if contextErr := debugThreadsContextError(ctx); contextErr != nil {
		return nil, contextErr
	}
	if err != nil {
		return nil, err
	}
	capacity := debugThreadStackPageSize
	if levels > 0 && levels < capacity {
		capacity = levels
	}
	frames := make([]StackFrame, 0, capacity)
	seenFrameIDs := make(map[int]struct{})
	requestStart := startFrame
	totalFrames := 0
	for {
		if err := debugThreadsContextError(ctx); err != nil {
			return nil, err
		}
		requestLevels := debugThreadStackPageSize
		if levels > 0 {
			remaining := levels - len(frames)
			if remaining <= 0 {
				break
			}
			if remaining < requestLevels {
				requestLevels = remaining
			}
		}
		body, requestErr := s.backend.Request(snapshot.Identity(), "stackTrace", map[string]any{
			"threadId":   threadID,
			"startFrame": requestStart,
			"levels":     requestLevels,
		})
		if err := debugThreadsContextError(ctx); err != nil {
			return nil, err
		}
		if requestErr != nil {
			return nil, fmt.Errorf("load stack for thread %d: %w", threadID, requestErr)
		}
		page, pageTotal, parseErr := parseThreadStackPage(body)
		if parseErr != nil {
			return nil, parseErr
		}
		for _, frame := range page {
			if _, duplicate := seenFrameIDs[frame.ID]; duplicate {
				return nil, fmt.Errorf("stackTrace response repeats frame id %d across pages", frame.ID)
			}
			seenFrameIDs[frame.ID] = struct{}{}
		}
		if levels > 0 && len(page) > levels-len(frames) {
			page = page[:levels-len(frames)]
		}
		if pageTotal > totalFrames {
			totalFrames = pageTotal
		}
		frames = append(frames, page...)
		if len(frames) > debugThreadMaxStackFrames {
			return nil, fmt.Errorf("stack for thread %d exceeds %d frames", threadID, debugThreadMaxStackFrames)
		}
		loadedThrough := requestStart + len(page)
		if len(page) == 0 || (totalFrames > 0 && loadedThrough >= totalFrames) {
			break
		}
		if levels > 0 && len(frames) >= levels {
			break
		}
		if totalFrames == 0 && len(page) < requestLevels {
			break
		}
		requestStart = loadedThrough
	}

	s.mu.RLock()
	selected := snapshot.ThreadID == threadID || s.selected[snapshot.SessionID] == threadID
	s.mu.RUnlock()
	var update *DebugThreadsSessionUpdate
	if selected && startFrame == 0 {
		update = &DebugThreadsSessionUpdate{
			Stack:        toDebugStackFrames(frames),
			ReplaceStack: true,
			StackTotal:   maxInt(totalFrames, startFrame+len(frames)),
			StackHasMore: stackPageHasMore(totalFrames, startFrame, levels, len(frames)),
		}
	}
	cacheChanged := false
	applied, err := s.commitResponse(snapshot, revision, update, func() {
		thread := s.ensureThreadLocked(snapshot.SessionID, threadID)
		cacheChanged = s.applyStackFramePageLocked(
			snapshot.SessionID,
			threadID,
			startFrame,
			levels,
			frames,
			totalFrames,
		)
		if s.selected[snapshot.SessionID] == 0 && snapshot.ThreadID == threadID {
			s.selected[snapshot.SessionID] = threadID
			thread.Selected = true
		}
	})
	if err != nil {
		return nil, err
	}
	if !applied {
		return s.cachedThreadFramePage(snapshot.SessionID, threadID, startFrame, levels), nil
	}
	if cacheChanged {
		s.emitThreadsUpdated(snapshot.SessionID)
	}
	return cloneStackFrames(frames), nil
}

func (s *DebugThreadsService) SelectThread(ctx context.Context, sessionID string, threadID int) error {
	if err := debugThreadsContextError(ctx); err != nil {
		return err
	}
	if threadID <= 0 {
		return fmt.Errorf("thread id must be positive")
	}
	snapshot, _, err := s.captureSnapshot(sessionID)
	if contextErr := debugThreadsContextError(ctx); contextErr != nil {
		return contextErr
	}
	if err != nil {
		return err
	}
	if !s.hasCachedThread(snapshot.SessionID, threadID) {
		if _, err := s.ListThreads(ctx, snapshot.SessionID); err != nil {
			return err
		}
		if !s.hasCachedThread(snapshot.SessionID, threadID) {
			return fmt.Errorf("unknown thread %d in debug session %q", threadID, snapshot.SessionID)
		}
	}
	activeErr := s.backend.SetActiveSession(snapshot.SessionID)
	if err := debugThreadsContextError(ctx); err != nil {
		return err
	}
	if activeErr != nil {
		return activeErr
	}
	snapshot, revision, err := s.captureSnapshot(snapshot.SessionID)
	if contextErr := debugThreadsContextError(ctx); contextErr != nil {
		return contextErr
	}
	if err != nil {
		return err
	}
	s.mu.RLock()
	thread := s.threads[snapshot.SessionID][threadID]
	if thread == nil {
		s.mu.RUnlock()
		return fmt.Errorf("unknown thread %d in debug session %q", threadID, snapshot.SessionID)
	}
	frames := cloneStackFrames(thread.Frames)
	s.mu.RUnlock()
	update := DebugThreadsSessionUpdate{
		ThreadID:     intPointer(threadID),
		Stack:        toDebugStackFrames(frames),
		ReplaceStack: true,
		StackTotal:   len(frames),
		ClearLocals:  true,
	}
	applied, err := s.commitResponse(snapshot, revision, &update, func() {
		for candidateID, candidate := range s.threads[snapshot.SessionID] {
			candidate.Selected = candidateID == threadID
		}
		s.selected[snapshot.SessionID] = threadID
		s.bumpRevisionLocked(snapshot.SessionID)
	})
	if err != nil {
		return err
	}
	if !applied {
		return ErrDebugThreadsStaleState
	}
	s.emitEvent(DebugThreadSelectedEventName, DebugThreadSelectedEvent{SessionID: snapshot.SessionID, ThreadID: threadID})
	s.emitThreadsUpdated(snapshot.SessionID)
	return nil
}

func (s *DebugThreadsService) ContinueThread(ctx context.Context, sessionID string, threadID int) error {
	if err := debugThreadsContextError(ctx); err != nil {
		return err
	}
	if threadID <= 0 {
		return fmt.Errorf("thread id must be positive")
	}
	snapshot, revision, err := s.captureSnapshot(sessionID)
	if contextErr := debugThreadsContextError(ctx); contextErr != nil {
		return contextErr
	}
	if err != nil {
		return err
	}
	capabilities, capabilitiesKnown := s.capabilitiesForRun(snapshot.Identity())
	if !capabilitiesKnown || !capabilities.SupportsSingleThreadExecutionRequests {
		return fmt.Errorf("adapter does not support single-thread execution requests")
	}
	body, requestErr := s.backend.Request(snapshot.Identity(), "continue", map[string]any{
		"threadId":     threadID,
		"singleThread": true,
	})
	if err := debugThreadsContextError(ctx); err != nil {
		return err
	}
	if requestErr != nil {
		return fmt.Errorf("continue thread %d: %w", threadID, requestErr)
	}
	var response struct {
		AllThreadsContinued *bool `json:"allThreadsContinued"`
	}
	if len(body) > 0 && string(body) != "null" {
		if err := json.Unmarshal(body, &response); err != nil {
			return fmt.Errorf("parse continue response: %w", err)
		}
	}
	allContinued := optionalAllThreadsContinued(response.AllThreadsContinued)
	stoppedAfter := s.stoppedAfterContinue(snapshot.SessionID, threadID, allContinued)
	update := DebugThreadsSessionUpdate{
		ThreadID:        intPointer(threadID),
		Stopped:         boolPointer(stoppedAfter),
		ClearLocals:     true,
		ClearAsyncStack: true,
		ReplaceStack:    true,
	}
	if !stoppedAfter {
		update.StopReason = stringPointer("")
	}
	applied, err := s.commitResponse(snapshot, revision, &update, func() {
		s.applyContinuedStateLocked(snapshot.SessionID, threadID, allContinued)
		s.bumpRevisionLocked(snapshot.SessionID)
	})
	if err != nil {
		return err
	}
	if !applied {
		return nil
	}
	s.emitThreadsUpdated(snapshot.SessionID)
	return nil
}

// ContinueAllThreads resumes the complete debug session. DAP still requires a
// threadId for the continue request, so the active or first cached thread is
// used while singleThread is explicitly disabled.
func (s *DebugThreadsService) ContinueAllThreads(ctx context.Context, sessionID string) error {
	if err := debugThreadsContextError(ctx); err != nil {
		return err
	}
	snapshot, revision, threadID, err := s.captureControlThread(ctx, sessionID)
	if contextErr := debugThreadsContextError(ctx); contextErr != nil {
		return contextErr
	}
	if err != nil {
		return err
	}
	body, requestErr := s.backend.Request(snapshot.Identity(), "continue", map[string]any{
		"threadId":     threadID,
		"singleThread": false,
	})
	if err := debugThreadsContextError(ctx); err != nil {
		return err
	}
	if requestErr != nil {
		return fmt.Errorf("continue all threads: %w", requestErr)
	}
	var response struct {
		AllThreadsContinued *bool `json:"allThreadsContinued"`
	}
	if len(body) > 0 && string(body) != "null" {
		if err := json.Unmarshal(body, &response); err != nil {
			return fmt.Errorf("parse continue response: %w", err)
		}
	}
	if response.AllThreadsContinued != nil && !*response.AllThreadsContinued {
		return fmt.Errorf("adapter continued only thread %d", threadID)
	}
	update := DebugThreadsSessionUpdate{
		ThreadID:        intPointer(threadID),
		Stopped:         boolPointer(false),
		StopReason:      stringPointer(""),
		ClearLocals:     true,
		ClearAsyncStack: true,
		ReplaceStack:    true,
	}
	applied, err := s.commitResponse(snapshot, revision, &update, func() {
		s.applyContinuedStateLocked(snapshot.SessionID, threadID, true)
		s.bumpRevisionLocked(snapshot.SessionID)
	})
	if err != nil {
		return err
	}
	if applied {
		s.emitThreadsUpdated(snapshot.SessionID)
	}
	return nil
}

// PauseAllThreads asks the adapter to suspend the complete debug session. The
// authoritative stopped state is applied by the subsequent DAP stopped event.
func (s *DebugThreadsService) PauseAllThreads(ctx context.Context, sessionID string) error {
	if err := debugThreadsContextError(ctx); err != nil {
		return err
	}
	snapshot, revision, threadID, err := s.captureControlThread(ctx, sessionID)
	if contextErr := debugThreadsContextError(ctx); contextErr != nil {
		return contextErr
	}
	if err != nil {
		return err
	}
	_, requestErr := s.backend.Request(snapshot.Identity(), "pause", map[string]any{
		"threadId": threadID,
	})
	if err := debugThreadsContextError(ctx); err != nil {
		return err
	}
	if requestErr != nil {
		return fmt.Errorf("pause all threads: %w", requestErr)
	}
	_, err = s.commitResponse(snapshot, revision, nil, func() {})
	return err
}

func (s *DebugThreadsService) StepThread(ctx context.Context, sessionID string, threadID int, stepType string) error {
	if err := debugThreadsContextError(ctx); err != nil {
		return err
	}
	if threadID <= 0 {
		return fmt.Errorf("thread id must be positive")
	}
	command, err := normalizeStepCommand(stepType)
	if err != nil {
		return err
	}
	snapshot, revision, err := s.captureSnapshot(sessionID)
	if contextErr := debugThreadsContextError(ctx); contextErr != nil {
		return contextErr
	}
	if err != nil {
		return err
	}
	capabilities, capabilitiesKnown := s.capabilitiesForRun(snapshot.Identity())
	if !capabilitiesKnown || !capabilities.SupportsSingleThreadExecutionRequests {
		return fmt.Errorf("adapter does not support single-thread execution requests")
	}
	_, requestErr := s.backend.Request(snapshot.Identity(), command, map[string]any{
		"threadId":     threadID,
		"singleThread": true,
	})
	if err := debugThreadsContextError(ctx); err != nil {
		return err
	}
	if requestErr != nil {
		return fmt.Errorf("%s thread %d: %w", command, threadID, requestErr)
	}
	stoppedAfter := s.stoppedAfterStep(snapshot.SessionID, threadID)
	update := DebugThreadsSessionUpdate{
		ThreadID:        intPointer(threadID),
		Stopped:         boolPointer(stoppedAfter),
		ClearLocals:     true,
		ClearAsyncStack: true,
		ReplaceStack:    true,
	}
	if !stoppedAfter {
		update.StopReason = stringPointer("")
	}
	applied, err := s.commitResponse(snapshot, revision, &update, func() {
		thread := s.ensureThreadLocked(snapshot.SessionID, threadID)
		thread.State = ThreadStateStepping
		thread.Frames = nil
		s.clearPendingStackPagesLocked(snapshot.SessionID, threadID)
		for candidateID, candidate := range s.threads[snapshot.SessionID] {
			candidate.Selected = candidateID == threadID
		}
		s.selected[snapshot.SessionID] = threadID
		s.allThreadsStopped[snapshot.SessionID] = false
		s.bumpRevisionLocked(snapshot.SessionID)
	})
	if err != nil {
		return err
	}
	if !applied {
		return nil
	}
	s.emitEvent(DebugThreadSelectedEventName, DebugThreadSelectedEvent{SessionID: snapshot.SessionID, ThreadID: threadID})
	s.emitThreadsUpdated(snapshot.SessionID)
	return nil
}

func (s *DebugThreadsService) SetCapabilities(sessionID string, capabilities DebugThreadsCapabilities) error {
	return s.setCapabilities(sessionID, capabilities, nil)
}

func (s *DebugThreadsService) GetCapabilities(sessionID string) (DebugThreadsCapabilities, error) {
	snapshot, _, err := s.captureSnapshot(sessionID)
	if err != nil {
		return DebugThreadsCapabilities{}, err
	}
	capabilities, _ := s.capabilitiesForRun(snapshot.Identity())
	return capabilities, nil
}

func (s *DebugThreadsService) CaptureRunToken(sessionID string) (DebugThreadsRunToken, error) {
	snapshot, _, err := s.captureSnapshot(sessionID)
	if err != nil {
		return DebugThreadsRunToken{}, err
	}
	return snapshot.Identity(), nil
}

func (s *DebugThreadsService) GetRunToken(sessionID string) (DebugThreadsRunToken, error) {
	return s.CaptureRunToken(sessionID)
}

func (s *DebugThreadsService) ApplyInitializeCapabilities(sessionID string, body json.RawMessage) error {
	return s.applyInitializeCapabilities(sessionID, body, nil)
}

func (s *DebugThreadsService) ApplyInitializeCapabilitiesForRun(
	sessionID string,
	runToken DebugThreadsRunToken,
	body json.RawMessage,
) error {
	return s.applyInitializeCapabilities(sessionID, body, &runToken)
}

func (s *DebugThreadsService) GetStepInTargets(sessionID string, frameID int) ([]StepInTarget, error) {
	if frameID <= 0 {
		return nil, fmt.Errorf("frame id must be positive")
	}
	snapshot, revision, err := s.captureSnapshot(sessionID)
	if err != nil {
		return nil, err
	}
	capabilities, capabilitiesKnown := s.capabilitiesForRun(snapshot.Identity())
	if !capabilitiesKnown || !capabilities.SupportsStepInTargetsRequest {
		return nil, fmt.Errorf("adapter does not support step-in targets")
	}
	body, err := s.backend.Request(snapshot.Identity(), "stepInTargets", map[string]any{"frameId": frameID})
	if err != nil {
		return nil, fmt.Errorf("get step-in targets: %w", err)
	}
	var response struct {
		Targets []StepInTarget `json:"targets"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("parse stepInTargets response: %w", err)
	}
	applied, err := s.commitResponse(snapshot, revision, nil, func() {})
	if err != nil {
		return nil, err
	}
	if !applied {
		return nil, ErrDebugThreadsStaleState
	}
	return append([]StepInTarget(nil), response.Targets...), nil
}

func (s *DebugThreadsService) Terminate(sessionID string, restart bool) error {
	snapshot, revision, err := s.captureSnapshot(sessionID)
	if err != nil {
		return err
	}
	capabilities, capabilitiesKnown := s.capabilitiesForRun(snapshot.Identity())
	if !capabilitiesKnown || !capabilities.SupportsTerminateRequest {
		return fmt.Errorf("adapter does not support terminate request")
	}
	if _, err := s.backend.Request(snapshot.Identity(), "terminate", map[string]any{"restart": restart}); err != nil {
		return fmt.Errorf("terminate debug session %q: %w", snapshot.SessionID, err)
	}
	update := DebugThreadsSessionUpdate{
		Stopped:      boolPointer(false),
		StopReason:   stringPointer("terminated"),
		ReplaceStack: true,
		ClearLocals:  true,
	}
	applied, err := s.commitResponse(snapshot, revision, &update, func() {
		binding := s.runs[snapshot.SessionID]
		binding.revision++
		s.clearRunStateLocked(snapshot.SessionID)
		s.runs[snapshot.SessionID] = binding
	})
	if err != nil {
		return err
	}
	if !applied {
		return nil
	}
	s.emitThreadsUpdated(snapshot.SessionID)
	return nil
}

func (s *DebugThreadsService) HandleStoppedEvent(sessionID string, event DebugStoppedEvent) error {
	return s.handleStoppedEvent(sessionID, event, nil)
}

func (s *DebugThreadsService) HandleStopped(sessionID string, threadID int, allThreadsStopped bool) error {
	return s.HandleStoppedEvent(sessionID, DebugStoppedEvent{
		ThreadID:          threadID,
		AllThreadsStopped: allThreadsStopped,
	})
}

func (s *DebugThreadsService) HandleContinuedEvent(sessionID string, event DebugContinuedEvent) error {
	return s.handleContinuedEvent(sessionID, event, nil)
}

func (s *DebugThreadsService) HandleThreadEvent(sessionID, reason string, threadID int) error {
	return s.handleThreadEvent(sessionID, reason, threadID, nil)
}

// HandleDAPEvent applies an event to the run current when called. Reader
// integrations should use HandleDAPEventForRun with a captured token.
func (s *DebugThreadsService) HandleDAPEvent(sessionID, eventName string, body json.RawMessage) error {
	return s.handleDAPEvent(sessionID, eventName, body, nil)
}

func (s *DebugThreadsService) HandleDAPEventForRun(
	sessionID string,
	runToken DebugThreadsRunToken,
	eventName string,
	body json.RawMessage,
) error {
	return s.handleDAPEvent(sessionID, eventName, body, &runToken)
}

func (s *DebugThreadsService) handleStoppedEvent(
	sessionID string,
	event DebugStoppedEvent,
	expected *DebugThreadsRunToken,
) error {
	if event.ThreadID < 0 {
		return fmt.Errorf("thread id must not be negative")
	}
	update := DebugThreadsSessionUpdate{
		Stopped:         boolPointer(true),
		ReplaceStack:    true,
		ClearLocals:     true,
		ClearAsyncStack: true,
		Touch:           true,
	}
	if event.ThreadID > 0 {
		update.ThreadID = intPointer(event.ThreadID)
	}
	if event.Reason != "" {
		update.StopReason = stringPointer(event.Reason)
	}
	sessionID, err := s.applyAuthoritativeUpdate(sessionID, expected, update, func(canonical string) {
		if event.AllThreadsStopped {
			s.clearPendingStackPagesLocked(canonical, 0)
			for _, thread := range s.threads[canonical] {
				thread.State = ThreadStateStopped
				thread.Frames = nil
			}
		} else if event.ThreadID == 0 {
			s.clearPendingStackPagesLocked(canonical, 0)
			for _, thread := range s.threads[canonical] {
				thread.Frames = nil
			}
		}
		if event.ThreadID > 0 {
			s.clearPendingStackPagesLocked(canonical, event.ThreadID)
			thread := s.ensureThreadLocked(canonical, event.ThreadID)
			thread.State = ThreadStateStopped
			thread.Frames = nil
			for candidateID, candidate := range s.threads[canonical] {
				candidate.Selected = candidateID == event.ThreadID
			}
			s.selected[canonical] = event.ThreadID
		}
		s.allThreadsStopped[canonical] = event.AllThreadsStopped
	})
	if err != nil {
		return err
	}
	if event.ThreadID > 0 {
		s.emitEvent(DebugThreadSelectedEventName, DebugThreadSelectedEvent{SessionID: sessionID, ThreadID: event.ThreadID})
	}
	s.emitEvent(DebugThreadStoppedEventName, DebugThreadStoppedEvent{
		SessionID:         sessionID,
		ThreadID:          event.ThreadID,
		Reason:            event.Reason,
		AllThreadsStopped: event.AllThreadsStopped,
	})
	s.emitThreadsUpdated(sessionID)
	return nil
}

func (s *DebugThreadsService) handleContinuedEvent(
	sessionID string,
	event DebugContinuedEvent,
	expected *DebugThreadsRunToken,
) error {
	if event.ThreadID < 0 {
		return fmt.Errorf("thread id must not be negative")
	}
	allContinued := optionalAllThreadsContinued(event.AllThreadsContinued)
	if s == nil || s.backend == nil {
		return fmt.Errorf("debug threads service has no backend")
	}
	s.opMu.Lock()
	snapshot, err := s.backend.Snapshot(sessionID)
	if err != nil {
		s.opMu.Unlock()
		return err
	}
	if err := validateBackendSnapshot(snapshot); err != nil {
		s.opMu.Unlock()
		return err
	}
	s.mu.Lock()
	s.bindRunLocked(snapshot.Identity())
	s.mu.Unlock()
	if err := validateRunToken(snapshot, expected); err != nil {
		s.opMu.Unlock()
		return err
	}
	s.mu.Lock()
	s.bindRunLocked(snapshot.Identity())
	stopped := false
	if !allContinued {
		for id, thread := range s.threads[snapshot.SessionID] {
			if id != event.ThreadID && thread.State == ThreadStateStopped {
				stopped = true
				break
			}
		}
	}
	s.mu.Unlock()
	update := DebugThreadsSessionUpdate{Stopped: boolPointer(stopped), Touch: true}
	if event.ThreadID > 0 {
		update.ThreadID = intPointer(event.ThreadID)
	}
	if !stopped {
		update.StopReason = stringPointer("")
	}
	if err := s.backend.ApplySessionUpdate(snapshot, update); err != nil {
		s.opMu.Unlock()
		return err
	}
	s.mu.Lock()
	s.applyContinuedStateLocked(snapshot.SessionID, event.ThreadID, allContinued)
	s.bumpRevisionLocked(snapshot.SessionID)
	s.mu.Unlock()
	s.opMu.Unlock()
	s.emitThreadsUpdated(snapshot.SessionID)
	return nil
}

func (s *DebugThreadsService) handleThreadEvent(
	sessionID, reason string,
	threadID int,
	expected *DebugThreadsRunToken,
) error {
	if threadID <= 0 {
		return fmt.Errorf("thread id must be positive")
	}
	reason = strings.ToLower(strings.TrimSpace(reason))
	if reason != "started" && reason != "exited" {
		return fmt.Errorf("unsupported thread event reason %q", reason)
	}
	selectedExited := false
	replacementThreadID := 0
	canonical, err := s.applyAuthoritativeUpdateWithBuilder(
		sessionID,
		expected,
		func(snapshot DebugThreadsSessionSnapshot) DebugThreadsSessionUpdate {
			canonical := snapshot.SessionID
			update := DebugThreadsSessionUpdate{Touch: true}
			if reason != "exited" || (s.selected[canonical] != threadID && snapshot.ThreadID != threadID) {
				return update
			}
			selectedExited = true
			cachedSelected := s.selected[canonical]
			if cachedSelected != threadID && s.threads[canonical][cachedSelected] != nil {
				replacementThreadID = cachedSelected
			}
			for _, candidateID := range s.order[canonical] {
				if replacementThreadID == 0 && candidateID != threadID && s.threads[canonical][candidateID] != nil {
					replacementThreadID = candidateID
					break
				}
			}
			update.ThreadID = intPointer(replacementThreadID)
			update.ReplaceStack = true
			update.ClearLocals = true
			update.ClearAsyncStack = true
			return update
		},
		func(canonical string) {
			switch reason {
			case "started":
				s.ensureThreadLocked(canonical, threadID)
			case "exited":
				delete(s.threads[canonical], threadID)
				s.clearPendingStackPagesLocked(canonical, threadID)
				s.order[canonical] = removeThreadID(s.order[canonical], threadID)
				if selectedExited {
					s.selected[canonical] = replacementThreadID
					for candidateID, candidate := range s.threads[canonical] {
						candidate.Selected = candidateID == replacementThreadID
					}
				}
			}
		},
	)
	if err != nil {
		return err
	}
	if selectedExited && replacementThreadID > 0 {
		s.emitEvent(DebugThreadSelectedEventName, DebugThreadSelectedEvent{
			SessionID: canonical,
			ThreadID:  replacementThreadID,
		})
	}
	s.emitThreadsUpdated(canonical)
	return nil
}

func (s *DebugThreadsService) handleDAPEvent(
	sessionID, eventName string,
	body json.RawMessage,
	expected *DebugThreadsRunToken,
) error {
	switch strings.ToLower(strings.TrimSpace(eventName)) {
	case "stopped":
		var event DebugStoppedEvent
		if err := json.Unmarshal(body, &event); err != nil {
			return fmt.Errorf("parse stopped event: %w", err)
		}
		return s.handleStoppedEvent(sessionID, event, expected)
	case "continued":
		var event DebugContinuedEvent
		if err := json.Unmarshal(body, &event); err != nil {
			return fmt.Errorf("parse continued event: %w", err)
		}
		return s.handleContinuedEvent(sessionID, event, expected)
	case "thread":
		var event struct {
			Reason   string `json:"reason"`
			ThreadID int    `json:"threadId"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			return fmt.Errorf("parse thread event: %w", err)
		}
		return s.handleThreadEvent(sessionID, event.Reason, event.ThreadID, expected)
	default:
		return fmt.Errorf("unsupported DAP thread event %q", eventName)
	}
}

func (s *DebugThreadsService) setCapabilities(
	sessionID string,
	capabilities DebugThreadsCapabilities,
	expected *DebugThreadsRunToken,
) error {
	if s == nil || s.backend == nil {
		return fmt.Errorf("debug threads service has no backend")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	snapshot, err := s.backend.Snapshot(sessionID)
	if err != nil {
		return err
	}
	if err := validateBackendSnapshot(snapshot); err != nil {
		return err
	}
	s.mu.Lock()
	s.bindRunLocked(snapshot.Identity())
	s.mu.Unlock()
	if err := validateRunToken(snapshot, expected); err != nil {
		return err
	}
	capabilities = normalizeThreadCapabilities(capabilities)
	s.mu.Lock()
	s.bindRunLocked(snapshot.Identity())
	s.capabilities[snapshot.SessionID] = capabilities
	s.capabilitiesKnown[snapshot.SessionID] = true
	s.mu.Unlock()
	return nil
}

func (s *DebugThreadsService) applyInitializeCapabilities(
	sessionID string,
	body json.RawMessage,
	expected *DebugThreadsRunToken,
) error {
	var capabilities DebugThreadsCapabilities
	if err := json.Unmarshal(body, &capabilities); err != nil {
		return fmt.Errorf("parse initialize capabilities: %w", err)
	}
	return s.setCapabilities(sessionID, capabilities, expected)
}

func (s *DebugThreadsService) captureSnapshot(sessionID string) (DebugThreadsSessionSnapshot, uint64, error) {
	if s == nil || s.backend == nil {
		return DebugThreadsSessionSnapshot{}, 0, fmt.Errorf("debug threads service has no backend")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	snapshot, err := s.backend.Snapshot(sessionID)
	if err != nil {
		return DebugThreadsSessionSnapshot{}, 0, err
	}
	if err := validateBackendSnapshot(snapshot); err != nil {
		return DebugThreadsSessionSnapshot{}, 0, err
	}
	s.mu.Lock()
	s.bindRunLocked(snapshot.Identity())
	revision := s.runs[snapshot.SessionID].revision
	s.mu.Unlock()
	return snapshot, revision, nil
}

func (s *DebugThreadsService) captureControlThread(
	ctx context.Context,
	sessionID string,
) (DebugThreadsSessionSnapshot, uint64, int, error) {
	snapshot, revision, err := s.captureSnapshot(sessionID)
	if err != nil {
		return DebugThreadsSessionSnapshot{}, 0, 0, err
	}
	if threadID := s.controlThreadID(snapshot); threadID > 0 {
		return snapshot, revision, threadID, nil
	}
	if _, err := s.ListThreads(ctx, snapshot.SessionID); err != nil {
		return DebugThreadsSessionSnapshot{}, 0, 0, err
	}
	snapshot, revision, err = s.captureSnapshot(snapshot.SessionID)
	if err != nil {
		return DebugThreadsSessionSnapshot{}, 0, 0, err
	}
	threadID := s.controlThreadID(snapshot)
	if threadID <= 0 {
		return DebugThreadsSessionSnapshot{}, 0, 0, fmt.Errorf(
			"debug session %q has no threads",
			snapshot.SessionID,
		)
	}
	return snapshot, revision, threadID, nil
}

func (s *DebugThreadsService) controlThreadID(snapshot DebugThreadsSessionSnapshot) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if selected := s.selected[snapshot.SessionID]; selected > 0 {
		return selected
	}
	if snapshot.ThreadID > 0 {
		return snapshot.ThreadID
	}
	for _, threadID := range s.order[snapshot.SessionID] {
		if threadID > 0 {
			return threadID
		}
	}
	return 0
}

func (s *DebugThreadsService) commitResponse(
	expected DebugThreadsSessionSnapshot,
	expectedRevision uint64,
	update *DebugThreadsSessionUpdate,
	applyCache func(),
) (bool, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	current, err := s.backend.Snapshot(expected.SessionID)
	if err != nil {
		return false, err
	}
	if err := validateBackendSnapshot(current); err != nil {
		return false, err
	}
	s.mu.Lock()
	s.bindRunLocked(current.Identity())
	s.mu.Unlock()
	if !sameRun(current.Identity(), expected.Identity()) {
		return false, ErrDebugThreadsStaleRun
	}
	if current.StateRevision != expected.StateRevision {
		return false, nil
	}
	s.mu.RLock()
	binding, exists := s.runs[expected.SessionID]
	validCache := exists && sameRun(binding.identity, expected.Identity()) && binding.revision == expectedRevision
	s.mu.RUnlock()
	if !validCache {
		return false, nil
	}
	if update != nil {
		if err := s.backend.ApplySessionUpdate(current, *update); err != nil {
			if isStaleBackendError(err) {
				return false, nil
			}
			return false, err
		}
	}
	s.mu.Lock()
	binding, exists = s.runs[expected.SessionID]
	if !exists || !sameRun(binding.identity, expected.Identity()) || binding.revision != expectedRevision {
		s.mu.Unlock()
		return false, nil
	}
	applyCache()
	s.mu.Unlock()
	return true, nil
}

func (s *DebugThreadsService) applyAuthoritativeUpdate(
	sessionID string,
	expected *DebugThreadsRunToken,
	update DebugThreadsSessionUpdate,
	applyCache func(canonical string),
) (string, error) {
	return s.applyAuthoritativeUpdateWithBuilder(
		sessionID,
		expected,
		func(DebugThreadsSessionSnapshot) DebugThreadsSessionUpdate { return update },
		applyCache,
	)
}

func (s *DebugThreadsService) applyAuthoritativeUpdateWithBuilder(
	sessionID string,
	expected *DebugThreadsRunToken,
	buildUpdate func(snapshot DebugThreadsSessionSnapshot) DebugThreadsSessionUpdate,
	applyCache func(canonical string),
) (string, error) {
	if s == nil || s.backend == nil {
		return "", fmt.Errorf("debug threads service has no backend")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	snapshot, err := s.backend.Snapshot(sessionID)
	if err != nil {
		return "", err
	}
	if err := validateBackendSnapshot(snapshot); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.bindRunLocked(snapshot.Identity())
	update := buildUpdate(snapshot)
	s.mu.Unlock()
	if err := validateRunToken(snapshot, expected); err != nil {
		return "", err
	}
	if err := s.backend.ApplySessionUpdate(snapshot, update); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.bindRunLocked(snapshot.Identity())
	applyCache(snapshot.SessionID)
	s.bumpRevisionLocked(snapshot.SessionID)
	s.mu.Unlock()
	return snapshot.SessionID, nil
}

func (s *DebugThreadsService) bindRunLocked(identity DebugThreadsRunIdentity) {
	s.ensureMapsLocked()
	current, exists := s.runs[identity.SessionID]
	if !exists || !sameRun(current.identity, identity) {
		s.clearRunStateLocked(identity.SessionID)
		s.runs[identity.SessionID] = debugThreadsRunBinding{identity: identity}
	}
}

func (s *DebugThreadsService) clearRunStateLocked(sessionID string) {
	delete(s.threads, sessionID)
	delete(s.order, sessionID)
	delete(s.selected, sessionID)
	delete(s.allThreadsStopped, sessionID)
	delete(s.capabilities, sessionID)
	delete(s.capabilitiesKnown, sessionID)
	delete(s.pendingStackPages, sessionID)
}

func (s *DebugThreadsService) ensureMapsLocked() {
	if s.threads == nil {
		s.threads = make(map[string]map[int]*ThreadInfo)
	}
	if s.order == nil {
		s.order = make(map[string][]int)
	}
	if s.selected == nil {
		s.selected = make(map[string]int)
	}
	if s.allThreadsStopped == nil {
		s.allThreadsStopped = make(map[string]bool)
	}
	if s.capabilities == nil {
		s.capabilities = make(map[string]DebugThreadsCapabilities)
	}
	if s.capabilitiesKnown == nil {
		s.capabilitiesKnown = make(map[string]bool)
	}
	if s.pendingStackPages == nil {
		s.pendingStackPages = make(map[string]map[int]map[int]debugThreadStackPage)
	}
	if s.runs == nil {
		s.runs = make(map[string]debugThreadsRunBinding)
	}
}

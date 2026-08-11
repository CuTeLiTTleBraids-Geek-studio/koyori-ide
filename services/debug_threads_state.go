package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

func (s *DebugThreadsService) ensureThreadLocked(sessionID string, threadID int) *ThreadInfo {
	threads := s.threads[sessionID]
	if threads == nil {
		threads = make(map[int]*ThreadInfo)
		s.threads[sessionID] = threads
	}
	thread := threads[threadID]
	if thread == nil {
		thread = &ThreadInfo{ID: threadID, State: ThreadStateRunning}
		threads[threadID] = thread
		s.order[sessionID] = append(s.order[sessionID], threadID)
	}
	return thread
}

func (s *DebugThreadsService) bumpRevisionLocked(sessionID string) {
	binding := s.runs[sessionID]
	binding.revision++
	s.runs[sessionID] = binding
}

func (s *DebugThreadsService) capabilitiesForRun(identity DebugThreadsRunIdentity) (DebugThreadsCapabilities, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	binding, exists := s.runs[identity.SessionID]
	if !exists || !sameRun(binding.identity, identity) {
		return DebugThreadsCapabilities{}, false
	}
	return normalizeThreadCapabilities(s.capabilities[identity.SessionID]), s.capabilitiesKnown[identity.SessionID]
}

func (s *DebugThreadsService) hasCachedThread(sessionID string, threadID int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.threads[sessionID][threadID] != nil
}

func (s *DebugThreadsService) cachedThreads(sessionID string) []ThreadInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cachedThreadsLocked(sessionID)
}

func (s *DebugThreadsService) cachedThreadFrames(sessionID string, threadID int) []StackFrame {
	s.mu.RLock()
	defer s.mu.RUnlock()
	thread := s.threads[sessionID][threadID]
	if thread == nil {
		return nil
	}
	return cloneStackFrames(thread.Frames)
}

func (s *DebugThreadsService) cachedThreadFramePage(sessionID string, threadID, startFrame, levels int) []StackFrame {
	s.mu.RLock()
	defer s.mu.RUnlock()
	thread := s.threads[sessionID][threadID]
	if thread == nil || startFrame >= len(thread.Frames) {
		return nil
	}
	endFrame := len(thread.Frames)
	if levels > 0 && startFrame+levels < endFrame {
		endFrame = startFrame + levels
	}
	return cloneStackFrames(thread.Frames[startFrame:endFrame])
}

func (s *DebugThreadsService) applyStackFramePageLocked(
	sessionID string,
	threadID, startFrame, levels int,
	frames []StackFrame,
	totalFrames int,
) bool {
	thread := s.ensureThreadLocked(sessionID, threadID)
	if startFrame == 0 && levels == 0 {
		thread.Frames = cloneStackFrames(frames)
		if sessions := s.pendingStackPages[sessionID]; sessions != nil {
			delete(sessions, threadID)
			if len(sessions) == 0 {
				delete(s.pendingStackPages, sessionID)
			}
		}
		return true
	}

	sessions := s.pendingStackPages[sessionID]
	if sessions == nil {
		sessions = make(map[int]map[int]debugThreadStackPage)
		s.pendingStackPages[sessionID] = sessions
	}
	pages := sessions[threadID]
	if pages == nil {
		pages = make(map[int]debugThreadStackPage)
		sessions[threadID] = pages
	}
	pages[startFrame] = debugThreadStackPage{levels: levels, frames: cloneStackFrames(frames), totalFrames: totalFrames}

	changed := false
	for {
		starts := make([]int, 0, len(pages))
		for pageStart := range pages {
			starts = append(starts, pageStart)
		}
		sort.Ints(starts)
		applied := false
		for _, pageStart := range starts {
			if pageStart > len(thread.Frames) {
				break
			}
			page := pages[pageStart]
			thread.Frames = mergeStackFramePage(thread.Frames, pageStart, page.levels, page.frames, page.totalFrames)
			delete(pages, pageStart)
			stackEnd := 0
			stackEndKnown := false
			if page.totalFrames > 0 {
				stackEnd = page.totalFrames
				stackEndKnown = true
			}
			if page.levels == 0 || len(page.frames) < page.levels {
				shortPageEnd := pageStart + len(page.frames)
				if !stackEndKnown || shortPageEnd < stackEnd {
					stackEnd = shortPageEnd
				}
				stackEndKnown = true
			}
			if stackEndKnown {
				for pendingStart := range pages {
					if pendingStart >= stackEnd {
						delete(pages, pendingStart)
					}
				}
			}
			changed = true
			applied = true
			break
		}
		if !applied {
			break
		}
	}
	if len(pages) == 0 {
		delete(sessions, threadID)
		if len(sessions) == 0 {
			delete(s.pendingStackPages, sessionID)
		}
	}
	return changed
}

func (s *DebugThreadsService) clearPendingStackPagesLocked(sessionID string, threadID int) {
	if threadID <= 0 {
		delete(s.pendingStackPages, sessionID)
		return
	}
	sessions := s.pendingStackPages[sessionID]
	if sessions == nil {
		return
	}
	delete(sessions, threadID)
	if len(sessions) == 0 {
		delete(s.pendingStackPages, sessionID)
	}
}

func (s *DebugThreadsService) cachedThreadsLocked(sessionID string) []ThreadInfo {
	threads := s.threads[sessionID]
	order := append([]int(nil), s.order[sessionID]...)
	if len(order) == 0 && len(threads) > 0 {
		for threadID := range threads {
			order = append(order, threadID)
		}
		sort.Ints(order)
	}
	result := make([]ThreadInfo, 0, len(order))
	for _, threadID := range order {
		if thread := threads[threadID]; thread != nil {
			result = append(result, cloneThreadInfo(*thread))
		}
	}
	return result
}

func (s *DebugThreadsService) emitThreadsUpdated(sessionID string) {
	s.mu.RLock()
	payload := DebugThreadsUpdatedEvent{
		SessionID: sessionID, Threads: s.cachedThreadsLocked(sessionID),
		AllThreadsStopped: s.allThreadsStopped[sessionID],
	}
	s.mu.RUnlock()
	s.emitEvent(DebugThreadsUpdatedEventName, payload)
}

func (s *DebugThreadsService) emitEvent(name string, payload any) {
	if s == nil {
		return
	}
	s.mu.RLock()
	emit := s.emit
	app := s.app
	s.mu.RUnlock()
	if emit != nil {
		emit(name, payload)
	}
	if app != nil {
		app.Event.Emit(name, payload)
	}
}

func (s *DebugThreadsService) applyContinuedStateLocked(sessionID string, threadID int, allContinued bool) {
	if allContinued {
		s.clearPendingStackPagesLocked(sessionID, 0)
		for _, thread := range s.threads[sessionID] {
			thread.State = ThreadStateRunning
			thread.Frames = nil
		}
	} else if threadID > 0 {
		s.clearPendingStackPagesLocked(sessionID, threadID)
		thread := s.ensureThreadLocked(sessionID, threadID)
		thread.State = ThreadStateRunning
		thread.Frames = nil
	}
	s.allThreadsStopped[sessionID] = false
}

func (s *DebugThreadsService) stoppedAfterContinue(sessionID string, threadID int, allContinued bool) bool {
	if allContinued {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, thread := range s.threads[sessionID] {
		if id != threadID && thread.State == ThreadStateStopped {
			return true
		}
	}
	return false
}

func (s *DebugThreadsService) stoppedAfterStep(sessionID string, threadID int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, thread := range s.threads[sessionID] {
		if id != threadID && thread.State == ThreadStateStopped {
			return true
		}
	}
	return false
}

func validateBackendSnapshot(snapshot DebugThreadsSessionSnapshot) error {
	if snapshot.SessionID == "" {
		return fmt.Errorf("debug backend returned an empty session id")
	}
	if snapshot.RunID == "" {
		return fmt.Errorf("debug backend returned an empty run id")
	}
	return nil
}

func validateRunToken(snapshot DebugThreadsSessionSnapshot, token *DebugThreadsRunToken) error {
	if token == nil {
		return nil
	}
	if token.SessionID == "" || token.RunID == "" {
		return fmt.Errorf("invalid debug run token")
	}
	if !sameRun(snapshot.Identity(), *token) {
		return ErrDebugThreadsStaleRun
	}
	return nil
}

func sameRun(left, right DebugThreadsRunIdentity) bool {
	return left.SessionID == right.SessionID && left.RunID == right.RunID && left.Generation == right.Generation
}

func isStaleBackendError(err error) bool {
	return errors.Is(err, ErrDebugThreadsStaleRun) || errors.Is(err, ErrDebugThreadsStaleState)
}

func optionalAllThreadsContinued(value *bool) bool { return value == nil || *value }

func parseThreadStackPage(body json.RawMessage) ([]StackFrame, int, error) {
	var response struct {
		StackFrames []struct {
			ID               int             `json:"id"`
			Name             string          `json:"name"`
			Line             int             `json:"line"`
			Column           int             `json:"column"`
			EndLine          int             `json:"endLine"`
			EndColumn        int             `json:"endColumn"`
			PresentationHint string          `json:"presentationHint"`
			ModuleID         json.RawMessage `json:"moduleId"`
			Source           *struct {
				Path string `json:"path"`
				Name string `json:"name"`
			} `json:"source"`
		} `json:"stackFrames"`
		TotalFrames int `json:"totalFrames"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, 0, fmt.Errorf("parse stackTrace response: %w", err)
	}
	if response.TotalFrames < 0 {
		return nil, 0, fmt.Errorf("stackTrace response has a negative totalFrames value")
	}
	frames := make([]StackFrame, 0, len(response.StackFrames))
	seen := make(map[int]struct{}, len(response.StackFrames))
	for _, frame := range response.StackFrames {
		if frame.ID <= 0 {
			return nil, 0, fmt.Errorf("stackTrace response contains invalid frame id %d", frame.ID)
		}
		if _, duplicate := seen[frame.ID]; duplicate {
			return nil, 0, fmt.Errorf("stackTrace response contains duplicate frame id %d", frame.ID)
		}
		seen[frame.ID] = struct{}{}
		file := ""
		if frame.Source != nil {
			file = frame.Source.Path
			if file == "" {
				file = frame.Source.Name
			}
		}
		frames = append(frames, StackFrame{
			ID: frame.ID, Name: frame.Name, Source: file, File: file, Line: frame.Line, Column: frame.Column,
			EndLine: frame.EndLine, EndColumn: frame.EndColumn, Module: debugStackFrameModule(frame.ModuleID),
			PresentationHint: frame.PresentationHint,
		})
	}
	return frames, response.TotalFrames, nil
}

func debugStackFrameModule(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String()
	}
	return ""
}

func normalizeStepCommand(stepType string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(stepType)) {
	case "next", "over", "stepover", "step_over", "step-over":
		return "next", nil
	case "stepin", "in", "into", "step_into", "step-into":
		return "stepIn", nil
	case "stepout", "out", "step_out", "step-out":
		return "stepOut", nil
	default:
		return "", fmt.Errorf("unsupported step type %q", stepType)
	}
}

func normalizeThreadCapabilities(capabilities DebugThreadsCapabilities) DebugThreadsCapabilities {
	if capabilities.SupportsStepInTargets || capabilities.SupportsStepInTargetsRequest {
		capabilities.SupportsStepInTargets = true
		capabilities.SupportsStepInTargetsRequest = true
	}
	return capabilities
}

func normalizeThreadState(state string) string {
	switch state {
	case ThreadStateStopped, ThreadStateStepping:
		return state
	default:
		return ThreadStateRunning
	}
}

func cloneThreadMap(source map[int]*ThreadInfo) map[int]*ThreadInfo {
	result := make(map[int]*ThreadInfo, len(source))
	for threadID, thread := range source {
		if thread != nil {
			copy := cloneThreadInfo(*thread)
			result[threadID] = &copy
		}
	}
	return result
}

func cloneThreadInfo(thread ThreadInfo) ThreadInfo {
	thread.Frames = cloneStackFrames(thread.Frames)
	return thread
}

func cloneStackFrames(frames []StackFrame) []StackFrame { return append([]StackFrame(nil), frames...) }

func toDebugStackFrames(frames []StackFrame) []DebugStackFrame {
	result := make([]DebugStackFrame, 0, len(frames))
	for _, frame := range frames {
		file := frame.File
		if file == "" {
			file = frame.Source
		}
		result = append(result, DebugStackFrame{
			ID: frame.ID, Name: frame.Name, File: file, Line: frame.Line, Column: frame.Column,
			PresentationHint: frame.PresentationHint, AsyncBoundary: frame.AsyncBoundary,
		})
	}
	return result
}

func fromDebugStackFrames(frames []DebugStackFrame) []StackFrame {
	result := make([]StackFrame, 0, len(frames))
	for _, frame := range frames {
		result = append(result, StackFrame{
			ID: frame.ID, Name: frame.Name, Source: frame.File, File: frame.File, Line: frame.Line,
			Column: frame.Column, PresentationHint: frame.PresentationHint, AsyncBoundary: frame.AsyncBoundary,
		})
	}
	return result
}

func removeThreadID(ids []int, target int) []int {
	result := ids[:0]
	for _, id := range ids {
		if id != target {
			result = append(result, id)
		}
	}
	return result
}

func debugThreadsContextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("context must not be nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func stackPageHasMore(totalFrames, startFrame, levels, frameCount int) bool {
	if totalFrames > 0 {
		return startFrame+frameCount < totalFrames
	}
	return levels > 0 && frameCount >= levels
}

func mergeStackFramePage(existing []StackFrame, startFrame, levels int, page []StackFrame, totalFrames int) []StackFrame {
	if startFrame > len(existing) {
		return cloneStackFrames(existing)
	}
	preserveSuffix := levels > 0 && len(page) >= levels
	replaceEnd := startFrame + len(page)
	if preserveSuffix {
		replaceEnd = startFrame + levels
	}
	capacity := startFrame + len(page)
	if preserveSuffix && replaceEnd < len(existing) {
		capacity += len(existing) - replaceEnd
	}
	merged := make([]StackFrame, 0, capacity)
	merged = append(merged, existing[:startFrame]...)
	merged = append(merged, page...)
	if preserveSuffix && replaceEnd < len(existing) {
		merged = append(merged, existing[replaceEnd:]...)
	}
	if totalFrames > 0 && len(merged) > totalFrames {
		merged = merged[:totalFrames]
	}
	return deduplicateStackFrames(merged)
}

func deduplicateStackFrames(frames []StackFrame) []StackFrame {
	seen := make(map[int]struct{}, len(frames))
	result := make([]StackFrame, 0, len(frames))
	for _, frame := range frames {
		if frame.ID <= 0 {
			continue
		}
		if _, duplicate := seen[frame.ID]; duplicate {
			continue
		}
		seen[frame.ID] = struct{}{}
		result = append(result, frame)
	}
	return result
}

func boolPointer(value bool) *bool       { return &value }
func intPointer(value int) *int          { return &value }
func stringPointer(value string) *string { return &value }

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

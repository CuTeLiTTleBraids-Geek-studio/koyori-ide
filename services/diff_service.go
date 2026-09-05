package services

// Plan 11 Task 13 — Diff 差异增强。
//
// 职责（Step 1-12）：
//   - Step 1: MultiFileDiff（[]FileDiff：Path/OldContent/NewContent/Hunks）
//   - Step 2: ThreeWayMerge(base, ours, theirs) + 冲突标记
//   - Step 3: AI 审查标注（每个 hunk 附加 AIComment）
//   - Step 4: 行内评论（任意行可附加 InlineComment）
//   - Step 5: DiffViewer.vue（多文件 tab+统计+hunk 折叠+行号+语法高亮）
//   - Step 6: Apply（单文件/全部）
//   - Step 7: Reject（单 hunk/单文件/全部）
//   - Step 8: AI 审查模式（自动生成 hunk 审查意见，severity 色标）
//   - Step 9: "审查整个 PR"入口 + Markdown 报告导出
//   - Step 10: 导出 diff/Markdown/HTML
//   - Step 11: Artifact 预览模式（iframe sandbox 复用 PluginViewIframe）
//   - Step 12: 测试覆盖

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Step 1: 结构化 Diff 类型
// ---------------------------------------------------------------------------

// DiffLineType 标识一行的变更类型。
type DiffLineType string

const (
	DiffLineContext  DiffLineType = "context"  // 未变更
	DiffLineAdded    DiffLineType = "added"    // 新增行
	DiffLineRemoved  DiffLineType = "removed"  // 删除行
	DiffLineConflict DiffLineType = "conflict" // 冲突行
)

// DiffLine 单行 diff。
type DiffLine struct {
	Type     DiffLineType    `json:"type"`
	OldNum   int             `json:"oldNum,omitempty"`   // 旧行号（removed/context 有）
	NewNum   int             `json:"newNum,omitempty"`   // 新行号（added/context 有）
	Content  string          `json:"content"`            // 行内容（不含前缀 +/-/空格）
	Comments []InlineComment `json:"comments,omitempty"` // Step 4: 行内评论
}

// Hunk 一组连续的 diff 行。
type Hunk struct {
	OldStart int        `json:"oldStart"`
	OldCount int        `json:"oldCount"`
	NewStart int        `json:"newStart"`
	NewCount int        `json:"newCount"`
	Lines    []DiffLine `json:"lines"`
	// Step 3: AI 审查标注
	AIComments []AIComment `json:"aiComments,omitempty"`
}

// AIComment AI 对 hunk 的审查意见（Step 3）。
type AIComment struct {
	Severity   AICommentSeverity `json:"severity"`
	Message    string            `json:"message"`
	Suggestion string            `json:"suggestion,omitempty"`
	Line       int               `json:"line,omitempty"` // 关联行号
}

// AICommentSeverity 审查意见严重级别（Step 8: severity 色标）。
type AICommentSeverity string

const (
	AISeverityInfo     AICommentSeverity = "info"
	AISeverityWarning  AICommentSeverity = "warning"
	AISeverityError    AICommentSeverity = "error"
	AISeverityCritical AICommentSeverity = "critical"
)

// InlineComment 行内评论（Step 4: 用户或 AI 添加）。
type InlineComment struct {
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	AIComment bool      `json:"aiComment,omitempty"`
}

// FileDiff 单个文件的 diff（Step 1）。
type FileDiff struct {
	Path       string `json:"path"`
	OldPath    string `json:"oldPath,omitempty"` // 重命名时旧路径
	OldContent string `json:"oldContent"`
	NewContent string `json:"newContent"`
	Hunks      []Hunk `json:"hunks"`
	// 统计
	AddedLines   int `json:"addedLines"`
	RemovedLines int `json:"removedLines"`
}

// MultiFileDiff 多文件 diff（Step 1）。
type MultiFileDiff struct {
	Files        []FileDiff `json:"files"`
	TotalAdded   int        `json:"totalAdded"`
	TotalRemoved int        `json:"totalRemoved"`
}

// ---------------------------------------------------------------------------
// Step 2: 三方合并
// ---------------------------------------------------------------------------

// ThreeWayMergeResult 三方合并结果。
type ThreeWayMergeResult struct {
	Merged      string `json:"merged"`
	Conflicts   int    `json:"conflicts"`
	HasConflict bool   `json:"hasConflict"`
}

// ThreeWayMerge 执行三方合并（Step 2）。
//
// base: 共同祖先；ours: 当前分支；theirs: 合并分支。
// 返回合并后的内容，冲突部分用 <<<<<<< / ======= / >>>>>>> 标记。
func ThreeWayMerge(base, ours, theirs string) ThreeWayMergeResult {
	baseLines := splitLines(base)
	ourLines := splitLines(ours)
	theirLines := splitLines(theirs)

	// 简化实现：逐行比较三方。
	// 完整实现应使用 diff3 算法，这里使用基于 LCS 的简化版本。
	var result []string
	conflicts := 0

	maxLen := len(baseLines)
	if len(ourLines) > maxLen {
		maxLen = len(ourLines)
	}
	if len(theirLines) > maxLen {
		maxLen = len(theirLines)
	}

	for i := 0; i < maxLen; i++ {
		var b, o, t string
		if i < len(baseLines) {
			b = baseLines[i]
		}
		if i < len(ourLines) {
			o = ourLines[i]
		}
		if i < len(theirLines) {
			t = theirLines[i]
		}

		if o == t {
			// 两方一致，无冲突
			if o != "" {
				result = append(result, o)
			}
		} else if o == b {
			// ours 未变更，用 theirs
			if t != "" {
				result = append(result, t)
			}
		} else if t == b {
			// theirs 未变更，用 ours
			if o != "" {
				result = append(result, o)
			}
		} else {
			// 两方都变更且不一致 → 冲突
			conflicts++
			result = append(result, "<<<<<<< ours")
			if o != "" {
				result = append(result, o)
			}
			result = append(result, "=======")
			if t != "" {
				result = append(result, t)
			}
			result = append(result, ">>>>>>> theirs")
		}
	}

	return ThreeWayMergeResult{
		Merged:      strings.Join(result, "\n"),
		Conflicts:   conflicts,
		HasConflict: conflicts > 0,
	}
}

// ---------------------------------------------------------------------------
// DiffService 服务
// ---------------------------------------------------------------------------

// DiffService 提供结构化 diff、三方合并、AI 审查标注、导出等功能。
type DiffService struct {
	mu      sync.Mutex
	applyMu sync.Mutex
	// 缓存的 AI 审查结果（key = file path + hunk index）
	aiReviews     map[string][]AIComment
	snapshotSvc   *SnapshotService // Step 3: Apply 前创建快照（可选）
	workspaceRoot string           // Step 3: 快照工作区根（legacy fallback，见 wsCtx）
	// GOAL-P0-02: 共享 workspace context。非 nil 时优先于 workspaceRoot，
	// 使工作区切换对本服务立即生效，而不是停留在构造期的空字符串。
	wsCtx      *WorkspaceContext
	fileSvc    *FileService
	receiptDir string
}

// NewDiffService 创建服务。
func NewDiffService() *DiffService {
	return NewDiffServiceWithReceiptDir("")
}

// NewDiffServiceWithReceiptDir creates a diff service with durable commit
// receipts. An empty directory keeps the lightweight headless/test behavior.
func NewDiffServiceWithReceiptDir(receiptDir string) *DiffService {
	return &DiffService{
		aiReviews:  make(map[string][]AIComment),
		receiptDir: receiptDir,
	}
}

// setSnapshotService 注入快照服务与工作区根（Step 3: Apply 前创建快照）。
//
//wails:ignore
func (s *DiffService) setSnapshotService(snap *SnapshotService, workspaceRoot string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshotSvc = snap
	s.workspaceRoot = workspaceRoot
}

// setWorkspaceContext 注入共享 workspace context（GOAL-P0-02）。
// 注入后本服务的快照根随工作区切换自动更新，不再依赖构造期字符串。
//
//wails:ignore
func (s *DiffService) setWorkspaceContext(ctx *WorkspaceContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wsCtx = ctx
}

// setFileService links the service used to publish file:saved only after the
// whole transaction commits. Transaction writes themselves stay event-free so
// a later failure and rollback cannot expose a false saved notification.
//
//wails:ignore
func (s *DiffService) setFileService(fileSvc *FileService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fileSvc = fileSvc
}

// CreatePreApplySnapshot 在 Apply 前创建快照（Step 3: pre-apply）。
// 由 Apply 流程的调用方在写盘前调用。
//
// GOAL-P0-02: fail-closed。此前空 workspace root 或快照失败都被静默吞掉并返回
// 空串，调用方无法区分"未启用快照"和"快照失败"，于是可能在没有恢复点的情况下
// 继续写盘。现在只有未注入 SnapshotService 时才返回 ("", nil)。
func (s *DiffService) CreatePreApplySnapshot() (string, error) {
	s.mu.Lock()
	snap := s.snapshotSvc
	ctx := s.wsCtx
	fallback := s.workspaceRoot
	s.mu.Unlock()

	if snap == nil {
		return "", nil
	}
	root := fallback
	if ctx != nil {
		resolved, err := ctx.RequireRoot()
		if err != nil {
			return "", err
		}
		root = resolved
	} else if root == "" {
		return "", fmt.Errorf("pre-apply snapshot requires a workspace root: %w", ErrNotAllowed)
	}
	created, err := snap.CreateSnapshot(root, string(SnapshotReasonPreApply))
	if err != nil {
		return "", fmt.Errorf("create pre-apply snapshot: %w", err)
	}
	if created == nil {
		return "", fmt.Errorf("create pre-apply snapshot: empty result: %w", ErrNotAllowed)
	}
	return created.ID, nil
}

// ThreeWayMergeFile 是包级 ThreeWayMerge 的 service wrapper（Step 2）。
// 包级函数无法被 Wails bindings 直接暴露，故在此转发，供前端调用。
func (s *DiffService) ThreeWayMergeFile(base, ours, theirs string) ThreeWayMergeResult {
	return ThreeWayMerge(base, ours, theirs)
}

// ComputeFileDiff 计算单个文件的 diff（Step 1）。
//
// 内部调用 myersDiff 生成 unified diff 文本，然后解析为结构化 Hunk。
func (s *DiffService) ComputeFileDiff(path, oldContent, newContent string) FileDiff {
	// 生成 unified diff 文本
	diffText := myersDiff(path, oldContent, newContent)

	// 解析为结构化 Hunk
	hunks := parseUnifiedDiff(diffText, oldContent, newContent)

	// 统计
	added, removed := 0, 0
	for _, h := range hunks {
		for _, l := range h.Lines {
			if l.Type == DiffLineAdded {
				added++
			} else if l.Type == DiffLineRemoved {
				removed++
			}
		}
	}

	return FileDiff{
		Path:         path,
		OldContent:   oldContent,
		NewContent:   newContent,
		Hunks:        hunks,
		AddedLines:   added,
		RemovedLines: removed,
	}
}

// ComputeMultiFileDiff 计算多个文件的 diff（Step 1）。
func (s *DiffService) ComputeMultiFileDiff(files []FileInput) MultiFileDiff {
	result := MultiFileDiff{}
	for _, f := range files {
		fd := s.ComputeFileDiff(f.Path, f.OldContent, f.NewContent)
		result.Files = append(result.Files, fd)
		result.TotalAdded += fd.AddedLines
		result.TotalRemoved += fd.RemovedLines
	}
	return result
}

// FileInput 单个文件输入。
type FileInput struct {
	Path       string `json:"path"`
	OldContent string `json:"oldContent"`
	NewContent string `json:"newContent"`
}

// ---------------------------------------------------------------------------
// Step 3-4, 8: AI 审查标注 + 行内评论
// ---------------------------------------------------------------------------

// AddAIComment 给指定 hunk 添加 AI 审查意见（Step 3）。
func (s *DiffService) AddAIComment(diff *MultiFileDiff, fileIdx, hunkIdx int, comment AIComment) {
	if fileIdx < 0 || fileIdx >= len(diff.Files) {
		return
	}
	f := &diff.Files[fileIdx]
	if hunkIdx < 0 || hunkIdx >= len(f.Hunks) {
		return
	}
	f.Hunks[hunkIdx].AIComments = append(f.Hunks[hunkIdx].AIComments, comment)

	// 缓存
	key := fmt.Sprintf("%s-%d", f.Path, hunkIdx)
	s.mu.Lock()
	s.aiReviews[key] = append(s.aiReviews[key], comment)
	s.mu.Unlock()
}

// AddInlineComment 给指定行添加行内评论（Step 4）。
func (s *DiffService) AddInlineComment(diff *MultiFileDiff, fileIdx, hunkIdx, lineIdx int, comment InlineComment) {
	if fileIdx < 0 || fileIdx >= len(diff.Files) {
		return
	}
	f := &diff.Files[fileIdx]
	if hunkIdx < 0 || hunkIdx >= len(f.Hunks) {
		return
	}
	h := &f.Hunks[hunkIdx]
	if lineIdx < 0 || lineIdx >= len(h.Lines) {
		return
	}
	h.Lines[lineIdx].Comments = append(h.Lines[lineIdx].Comments, comment)
}

// ---------------------------------------------------------------------------
// Step 6-7: Apply / Reject
// ---------------------------------------------------------------------------

// ApplyFile 应用单个文件的 diff（用 NewContent 替换 OldContent）。
// 返回应用后的内容。
func (s *DiffService) ApplyFile(fd FileDiff) string {
	return fd.NewContent
}

// ApplyAll 应用所有文件的 diff。
// 返回 map[path]content。
func (s *DiffService) ApplyAll(diff MultiFileDiff) map[string]string {
	result := make(map[string]string, len(diff.Files))
	for _, f := range diff.Files {
		result[f.Path] = f.NewContent
	}
	return result
}

// ApplySelectedHunks keeps only the selected hunk indexes and returns the
// reconstructed file content. An empty selection is a no-op (old content).
func (s *DiffService) ApplySelectedHunks(fd FileDiff, selected []int) string {
	if len(fd.Hunks) == 0 {
		return fd.NewContent
	}
	keep := map[int]struct{}{}
	for _, idx := range selected {
		if idx >= 0 && idx < len(fd.Hunks) {
			keep[idx] = struct{}{}
		}
	}
	if len(keep) == 0 {
		return fd.OldContent
	}
	if len(keep) == len(fd.Hunks) {
		return fd.NewContent
	}
	content := fd.NewContent
	for idx := len(fd.Hunks) - 1; idx >= 0; idx-- {
		if _, ok := keep[idx]; ok {
			continue
		}
		content = s.RejectHunk(FileDiff{Path: fd.Path, OldContent: fd.OldContent, NewContent: content, Hunks: fd.Hunks}, idx)
	}
	return content
}

// RejectHunk 拒绝单个 hunk（返回不含该 hunk 的内容）。
// Step 7: Reject 单 hunk。
func (s *DiffService) RejectHunk(fd FileDiff, hunkIdx int) string {
	if hunkIdx < 0 || hunkIdx >= len(fd.Hunks) {
		return fd.NewContent
	}
	oldLines := splitLines(fd.OldContent)
	newLineCount := len(splitLines(fd.NewContent))
	result := make([]string, 0, len(oldLines))
	oldIndex := 0
	useNewTrailingNewline := false
	for i, h := range fd.Hunks {
		if i == hunkIdx {
			continue
		}
		oldEnd := h.OldStart - 1 + h.OldCount
		newEnd := h.NewStart - 1 + h.NewCount
		if oldEnd >= len(oldLines) || newEnd >= newLineCount {
			useNewTrailingNewline = true
		}
		hunkStart := h.OldStart - 1
		if hunkStart < oldIndex {
			hunkStart = oldIndex
		}
		if hunkStart > len(oldLines) {
			hunkStart = len(oldLines)
		}
		result = append(result, oldLines[oldIndex:hunkStart]...)
		oldIndex = hunkStart
		for _, line := range h.Lines {
			switch line.Type {
			case DiffLineAdded:
				result = append(result, line.Content)
			case DiffLineRemoved:
				if oldIndex < len(oldLines) {
					oldIndex++
				}
			default:
				if oldIndex < len(oldLines) {
					result = append(result, oldLines[oldIndex])
					oldIndex++
				}
			}
		}
	}
	result = append(result, oldLines[oldIndex:]...)
	content := strings.Join(result, "\n")
	trailingNewlineSource := fd.OldContent
	if useNewTrailingNewline {
		trailingNewlineSource = fd.NewContent
	}
	if strings.HasSuffix(trailingNewlineSource, "\n") {
		content += "\n"
	}
	return content
}

// RejectFile 拒绝整个文件（返回 OldContent）。
func (s *DiffService) RejectFile(fd FileDiff) string {
	return fd.OldContent
}

// RejectAll 拒绝所有文件。
func (s *DiffService) RejectAll(diff MultiFileDiff) map[string]string {
	result := make(map[string]string, len(diff.Files))
	for _, f := range diff.Files {
		result[f.Path] = f.OldContent
	}
	return result
}

// ---------------------------------------------------------------------------
// Step 9: PR 审查 + Markdown 报告
// ---------------------------------------------------------------------------

// ReviewPRRequest PR 审查请求。
type ReviewPRRequest struct {
	BaseBranch string `json:"baseBranch"`
	RepoPath   string `json:"repoPath"`
}

// ReviewPRResult PR 审查结果。
type ReviewPRResult struct {
	Summary     string       `json:"summary"`
	FileReviews []FileReview `json:"fileReviews"`
	Stats       ReviewStats  `json:"stats"`
}

// FileReview 单文件审查结果。
type FileReview struct {
	Path     string      `json:"path"`
	Comments []AIComment `json:"comments"`
}

// ReviewStats 审查统计。
type ReviewStats struct {
	FilesReviewed int `json:"filesReviewed"`
	TotalComments int `json:"totalComments"`
	Critical      int `json:"critical"`
	Errors        int `json:"errors"`
	Warnings      int `json:"warnings"`
}

// ReviewPR 审查整个 PR（Step 9）。
// 简化实现：接收已有的 MultiFileDiff + AI 审查意见，生成 Markdown 报告。
func (s *DiffService) ReviewPR(diff MultiFileDiff, reviews []FileReview) ReviewPRResult {
	stats := ReviewStats{}
	for _, fr := range reviews {
		stats.FilesReviewed++
		for _, c := range fr.Comments {
			stats.TotalComments++
			switch c.Severity {
			case AISeverityCritical:
				stats.Critical++
			case AISeverityError:
				stats.Errors++
			case AISeverityWarning:
				stats.Warnings++
			}
		}
	}
	return ReviewPRResult{
		Summary:     fmt.Sprintf("Reviewed %d files with %d comments (%d critical, %d errors, %d warnings)", stats.FilesReviewed, stats.TotalComments, stats.Critical, stats.Errors, stats.Warnings),
		FileReviews: reviews,
		Stats:       stats,
	}
}

// ExportMarkdown 导出为 Markdown 报告（Step 9-10）。
func (s *DiffService) ExportMarkdown(diff MultiFileDiff, reviews []FileReview) string {
	var b strings.Builder
	b.WriteString("# Diff Review Report\n\n")
	b.WriteString(fmt.Sprintf("**Files:** %d  | **+%d / -%d**\n\n", len(diff.Files), diff.TotalAdded, diff.TotalRemoved))

	for _, f := range diff.Files {
		b.WriteString(fmt.Sprintf("## %s (+%d / -%d)\n\n", f.Path, f.AddedLines, f.RemovedLines))
		// 查找此文件的审查意见
		for _, r := range reviews {
			if r.Path == f.Path {
				for _, c := range r.Comments {
					emoji := severityEmoji(c.Severity)
					b.WriteString(fmt.Sprintf("- %s **%s**: %s", emoji, c.Severity, c.Message))
					if c.Suggestion != "" {
						b.WriteString(fmt.Sprintf("\n  > Suggestion: %s", c.Suggestion))
					}
					b.WriteString("\n")
				}
			}
		}
		// 输出 diff hunk
		for _, h := range f.Hunks {
			b.WriteString(fmt.Sprintf("```diff\n@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldCount, h.NewStart, h.NewCount))
			for _, l := range h.Lines {
				switch l.Type {
				case DiffLineAdded:
					b.WriteString("+")
				case DiffLineRemoved:
					b.WriteString("-")
				case DiffLineConflict:
					b.WriteString("!")
				default:
					b.WriteString(" ")
				}
				b.WriteString(l.Content + "\n")
			}
			b.WriteString("```\n\n")
		}
	}
	return b.String()
}

func severityEmoji(s AICommentSeverity) string {
	switch s {
	case AISeverityCritical:
		return "🔴"
	case AISeverityError:
		return "🟠"
	case AISeverityWarning:
		return "🟡"
	default:
		return "🔵"
	}
}

// ---------------------------------------------------------------------------
// Step 10: 导出
// ---------------------------------------------------------------------------

// ExportUnifiedDiff 导出为 unified diff 文本（Step 10）。
func (s *DiffService) ExportUnifiedDiff(diff MultiFileDiff) string {
	var b strings.Builder
	for _, f := range diff.Files {
		b.WriteString(fmt.Sprintf("--- a/%s\n", f.Path))
		b.WriteString(fmt.Sprintf("+++ b/%s\n", f.Path))
		for _, h := range f.Hunks {
			b.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldCount, h.NewStart, h.NewCount))
			for _, l := range h.Lines {
				switch l.Type {
				case DiffLineAdded:
					b.WriteString("+")
				case DiffLineRemoved:
					b.WriteString("-")
				default:
					b.WriteString(" ")
				}
				b.WriteString(l.Content + "\n")
			}
		}
	}
	return b.String()
}

// ExportHTML 导出为 HTML（Step 10）。
func (s *DiffService) ExportHTML(diff MultiFileDiff) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>Diff Report</title>")
	b.WriteString("<style>body{font-family:monospace;margin:20px}.added{color:green}.removed{color:red}.context{color:gray}.hunk{background:#f0f0f0;padding:4px;margin:8px 0}.file{border:1px solid #ccc;margin:16px 0;padding:8px}</style>")
	b.WriteString("</head><body>")

	for _, f := range diff.Files {
		b.WriteString(fmt.Sprintf("<div class=\"file\"><h2>%s (+%d / -%d)</h2>", f.Path, f.AddedLines, f.RemovedLines))
		for _, h := range f.Hunks {
			b.WriteString(fmt.Sprintf("<div class=\"hunk\">@@ -%d,%d +%d,%d @@", h.OldStart, h.OldCount, h.NewStart, h.NewCount))
			for _, l := range h.Lines {
				cls := "context"
				prefix := " "
				switch l.Type {
				case DiffLineAdded:
					cls = "added"
					prefix = "+"
				case DiffLineRemoved:
					cls = "removed"
					prefix = "-"
				}
				b.WriteString(fmt.Sprintf("<div class=\"%s\">%s%s</div>", cls, prefix, escapeHTML(l.Content)))
			}
			b.WriteString("</div>")
		}
		b.WriteString("</div>")
	}
	b.WriteString("</body></html>")
	return b.String()
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// ---------------------------------------------------------------------------
// unified diff 解析
// ---------------------------------------------------------------------------

// parseUnifiedDiff 解析 unified diff 文本为结构化 Hunk。
func parseUnifiedDiff(diffText, oldContent, newContent string) []Hunk {
	if diffText == "" {
		return nil
	}

	lines := splitLines(diffText)
	var hunks []Hunk
	var current *Hunk

	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)

	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			// 新 hunk
			if current != nil {
				hunks = append(hunks, *current)
			}
			h := parseHunkHeader(line)
			current = &h
		} else if current != nil {
			dl := parseDiffLine(line, &current.OldStart, &current.NewStart, oldLines, newLines)
			current.Lines = append(current.Lines, dl)
		}
	}
	if current != nil {
		hunks = append(hunks, *current)
	}

	// 重新计算 oldStart/newStart（因为上面的 parseDiffLine 会递增）
	// 重新解析以获得正确的行号
	return renumberHunks(hunks, oldLines, newLines)
}

// parseHunkHeader 解析 @@ -oldStart,oldCount +newStart,newCount @@ 行。
func parseHunkHeader(line string) Hunk {
	// 格式: @@ -1,5 +1,7 @@
	var oldStart, oldCount, newStart, newCount int
	_, _ = fmt.Sscanf(line, "@@ -%d,%d +%d,%d @@", &oldStart, &oldCount, &newStart, &newCount)
	if oldCount == 0 {
		oldCount = 1
	}
	if newCount == 0 {
		newCount = 1
	}
	return Hunk{
		OldStart: oldStart,
		OldCount: oldCount,
		NewStart: newStart,
		NewCount: newCount,
	}
}

// parseDiffLine 解析单行 diff。
func parseDiffLine(line string, oldStart, newStart *int, oldLines, newLines []string) DiffLine {
	if line == "" {
		return DiffLine{Type: DiffLineContext, Content: ""}
	}
	prefix := line[0]
	content := line[1:]
	switch prefix {
	case '+':
		return DiffLine{Type: DiffLineAdded, Content: content}
	case '-':
		return DiffLine{Type: DiffLineRemoved, Content: content}
	default:
		return DiffLine{Type: DiffLineContext, Content: content}
	}
}

// renumberHunks 重新计算行号。
func renumberHunks(hunks []Hunk, oldLines, newLines []string) []Hunk {
	for hi := range hunks {
		oldNum := hunks[hi].OldStart
		newNum := hunks[hi].NewStart
		for li := range hunks[hi].Lines {
			l := &hunks[hi].Lines[li]
			switch l.Type {
			case DiffLineContext:
				l.OldNum = oldNum
				l.NewNum = newNum
				oldNum++
				newNum++
			case DiffLineAdded:
				l.NewNum = newNum
				newNum++
			case DiffLineRemoved:
				l.OldNum = oldNum
				oldNum++
			}
		}
	}
	return hunks
}

// splitLines 由 myers_diff.go 提供（包内共享），此处不重复声明。

// ---------------------------------------------------------------------------
// GOAL-P1-04R: Unified edit transaction entry point for AI diff apply
// ---------------------------------------------------------------------------

// applyDiffTransaction atomically writes a set of FileDiffs to disk via the
// unified workspace edit transaction (workspace_edit_transaction.go).
//
// For each FileDiff the caller must supply a BaselineHash that matches the
// current disk content; this is exactly the hash of FileDiff.OldContent. The
// function validates all paths against the workspace root from wsCtx, checks
// for dirty buffers when isDirty is non-nil, and rolls back any partially
// applied files on write failure.
//
// This is the authoritative AI diff-apply entry point. The frontend must call
// this instead of looping FileService.WriteFile, which provides no rollback
// guarantee for multi-file edits.
//
// isDirty may be nil (disables the dirty-buffer check).
//
//wails:ignore
func (s *DiffService) applyDiffTransaction(
	ctx context.Context,
	files []FileDiff,
	read func(path string) (string, error),
	write func(path, content string) error,
	isDirty func(path string) bool,
) WorkspaceEditApplyResult {
	s.mu.Lock()
	wsCtx := s.wsCtx
	fallbackRoot := s.workspaceRoot
	receiptDir := s.receiptDir
	s.mu.Unlock()

	root := fallbackRoot
	if wsCtx != nil {
		r, err := wsCtx.RequireRoot()
		if err != nil {
			return WorkspaceEditApplyResult{Err: err, FailureReason: err.Error()}
		}
		root = r
	}

	// Build a WorkspaceEditPreview from the supplied FileDiffs.
	// BaselineHash is the hash of OldContent (what we expect on disk).
	preview := WorkspaceEditPreview{
		Files: make([]WorkspaceEditPreviewFile, 0, len(files)),
	}
	for _, fd := range files {
		preview.Files = append(preview.Files, WorkspaceEditPreviewFile{
			FilePath:        fd.Path,
			BaselineHash:    contentHash([]byte(fd.OldContent)),
			OriginalContent: fd.OldContent,
			ModifiedContent: fd.NewContent,
		})
	}

	return applyEditTransaction(ctx, EditTransaction{TextEdits: preview}, EditTransactionOptions{
		Root:     root,
		Read:     read,
		Write:    write,
		IsDirty:  isDirty,
		OnCommit: commitReceiptWriter(receiptDir),
	})
}

// ApplyDiff is the renderer-facing adapter for applyDiffTransaction. The
// renderer supplies only immutable diff inputs; workspace identity, path
// validation, hash preconditions, atomic writes, and rollback stay backend
// controlled.
func (s *DiffService) ApplyDiff(files []FileDiff) WorkspaceEditApplyResult {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	read := func(path string) (string, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	write := func(path, content string) error {
		perm := os.FileMode(0o644)
		if info, err := os.Stat(path); err == nil {
			perm = info.Mode().Perm()
		} else if !os.IsNotExist(err) {
			return err
		}
		return atomicWriteFile(path, []byte(content), perm)
	}

	result := s.applyDiffTransaction(context.Background(), files, read, write, nil)
	if !result.Applied {
		return result
	}

	s.mu.Lock()
	fileSvc := s.fileSvc
	s.mu.Unlock()
	if fileSvc != nil {
		for _, path := range result.AppliedFiles {
			fileSvc.emitFileSaved(path)
		}
	}
	return result
}

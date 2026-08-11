package services

import (
	"strings"
	"testing"
)

// F-8 (prompt-2.md 517-535 / task-2.md 87-95): LSP colorProvider /
// linkedEditingRange 后端方法测试。
//
// 共 6 个测试，分两组：
//   - NoServer × 3：使用 NewLSPService("") 创建无 server 的服务，验证返回
//     error 且 error 包含 "not_running"（task-2.md 105-108 重要约定：无 server
//     运行时返回 error，区别于现有优雅降级返回空切片）。
//   - ValidArgs × 3：使用 newMockLSPServer 注入 mock handler，验证响应解析
//     正确（Color/ColorPresentation/LinkedEditRange 字段映射）。
//
// mock 复用 lsp_service_test.go 中的 newMockLSPServer（注入 servers["go"]），
// 因此 ValidArgs 测试一律使用 .go URI。

// TestGetDocumentColors_NoServer 验证无 LSP server 运行时 GetDocumentColors
// 返回 error（而非空切片），且 error 包含 "not_running" 标记。
func TestGetDocumentColors_NoServer(t *testing.T) {
	svc := NewLSPService("") // 无任何 server
	got, err := svc.GetDocumentColors("file:///tmp/ws/main.go")
	if err == nil {
		t.Fatalf("GetDocumentColors: expected error when no server running, got nil err and %v results", got)
	}
	if !strings.Contains(err.Error(), "not_running") {
		t.Errorf("GetDocumentColors err = %q, want substring 'not_running'", err.Error())
	}
	if got != nil {
		t.Errorf("GetDocumentColors: expected nil slice on error, got %v", got)
	}
}

// TestGetDocumentColors_ValidUri 验证 mock LSP server 返回的 documentColor
// 响应被正确解析为 ColorInformation 列表（Range / Color 字段映射）。
func TestGetDocumentColors_ValidUri(t *testing.T) {
	svc := newMockLSPServer(t, map[string]func(params interface{}) interface{}{
		"textDocument/documentColor": func(params interface{}) interface{} {
			return []map[string]interface{}{
				{
					"range": map[string]interface{}{
						"start": map[string]int{"line": 0, "character": 1},
						"end":   map[string]int{"line": 0, "character": 7},
					},
					"color": map[string]interface{}{
						"red":   1.0,
						"green": 0.0,
						"blue":  0.0,
						"alpha": 1.0,
					},
				},
			}
		},
	})
	got, err := svc.GetDocumentColors("file:///tmp/ws/main.go")
	if err != nil {
		t.Fatalf("GetDocumentColors: unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("GetDocumentColors: got %d colors, want 1", len(got))
	}
	c := got[0]
	if c.Color.Red != 1.0 || c.Color.Green != 0.0 || c.Color.Blue != 0.0 || c.Color.Alpha != 1.0 {
		t.Errorf("GetDocumentColors color = %+v, want {1,0,0,1}", c.Color)
	}
	if c.Range.Start.Line != 0 || c.Range.Start.Character != 1 {
		t.Errorf("GetDocumentColors range.Start = %+v, want {0,1}", c.Range.Start)
	}
	if c.Range.End.Line != 0 || c.Range.End.Character != 7 {
		t.Errorf("GetDocumentColors range.End = %+v, want {0,7}", c.Range.End)
	}
}

// TestGetColorPresentations_NoServer 验证无 LSP server 运行时
// GetColorPresentations 返回 error 且 error 包含 "not_running"。
func TestGetColorPresentations_NoServer(t *testing.T) {
	svc := NewLSPService("")
	got, err := svc.GetColorPresentations("file:///tmp/ws/main.go", Color{1, 0, 0, 1}, LSPRange{
		Start: LSPPosition{Line: 0, Character: 1},
		End:   LSPPosition{Line: 0, Character: 7},
	})
	if err == nil {
		t.Fatalf("GetColorPresentations: expected error when no server running, got nil err and %v results", got)
	}
	if !strings.Contains(err.Error(), "not_running") {
		t.Errorf("GetColorPresentations err = %q, want substring 'not_running'", err.Error())
	}
	if got != nil {
		t.Errorf("GetColorPresentations: expected nil slice on error, got %v", got)
	}
}

// TestGetColorPresentations_ValidArgs 验证 mock LSP server 返回的
// colorPresentation 响应被正确解析为 ColorPresentation 列表（Label /
// TextEdit / AdditionalTextEdits 字段映射，TextEdit 从 LSP wire 格式
// 转换为扁平 TextEdit）。
func TestGetColorPresentations_ValidArgs(t *testing.T) {
	svc := newMockLSPServer(t, map[string]func(params interface{}) interface{}{
		"textDocument/colorPresentation": func(params interface{}) interface{} {
			return []map[string]interface{}{
				{
					"label": "#ff0000",
					"textEdit": map[string]interface{}{
						"range": map[string]interface{}{
							"start": map[string]int{"line": 0, "character": 1},
							"end":   map[string]int{"line": 0, "character": 7},
						},
						"newText": "#ff0000",
					},
					"additionalTextEdits": []map[string]interface{}{
						{
							"range": map[string]interface{}{
								"start": map[string]int{"line": 1, "character": 0},
								"end":   map[string]int{"line": 1, "character": 0},
							},
							"newText": "/* red */",
						},
					},
				},
			}
		},
	})
	got, err := svc.GetColorPresentations("file:///tmp/ws/main.go", Color{1, 0, 0, 1}, LSPRange{
		Start: LSPPosition{Line: 0, Character: 1},
		End:   LSPPosition{Line: 0, Character: 7},
	})
	if err != nil {
		t.Fatalf("GetColorPresentations: unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("GetColorPresentations: got %d presentations, want 1", len(got))
	}
	p := got[0]
	if p.Label != "#ff0000" {
		t.Errorf("GetColorPresentations label = %q, want '#ff0000'", p.Label)
	}
	if p.TextEdit == nil {
		t.Fatalf("GetColorPresentations: TextEdit is nil, want non-nil")
	}
	if p.TextEdit.NewText != "#ff0000" {
		t.Errorf("GetColorPresentations TextEdit.NewText = %q, want '#ff0000'", p.TextEdit.NewText)
	}
	if p.TextEdit.StartLine != 0 || p.TextEdit.StartCol != 1 || p.TextEdit.EndLine != 0 || p.TextEdit.EndCol != 7 {
		t.Errorf("GetColorPresentations TextEdit range = {startLine:%d startCol:%d endLine:%d endCol:%d}, want {0,1,0,7}",
			p.TextEdit.StartLine, p.TextEdit.StartCol, p.TextEdit.EndLine, p.TextEdit.EndCol)
	}
	if len(p.AdditionalTextEdits) != 1 {
		t.Fatalf("GetColorPresentations: got %d additional edits, want 1", len(p.AdditionalTextEdits))
	}
	ae := p.AdditionalTextEdits[0]
	if ae.NewText != "/* red */" {
		t.Errorf("GetColorPresentations additionalTextEdits[0].NewText = %q, want '/* red */'", ae.NewText)
	}
	if ae.StartLine != 1 || ae.StartCol != 0 || ae.EndLine != 1 || ae.EndCol != 0 {
		t.Errorf("GetColorPresentations additionalTextEdits[0] range = {startLine:%d startCol:%d endLine:%d endCol:%d}, want {1,0,1,0}",
			ae.StartLine, ae.StartCol, ae.EndLine, ae.EndCol)
	}
}

// TestPrepareLinkedEdits_NoServer 验证无 LSP server 运行时
// PrepareLinkedEdits 返回 error 且 error 包含 "not_running"。
func TestPrepareLinkedEdits_NoServer(t *testing.T) {
	svc := NewLSPService("")
	got, err := svc.PrepareLinkedEdits("file:///tmp/ws/main.go", LSPPosition{Line: 0, Character: 1})
	if err == nil {
		t.Fatalf("PrepareLinkedEdits: expected error when no server running, got nil err and %v results", got)
	}
	if !strings.Contains(err.Error(), "not_running") {
		t.Errorf("PrepareLinkedEdits err = %q, want substring 'not_running'", err.Error())
	}
	if got != nil {
		t.Errorf("PrepareLinkedEdits: expected nil slice on error, got %v", got)
	}
}

// TestPrepareLinkedEdits_ValidArgs 验证 mock LSP server 返回的
// prepareLinkedEdits 响应（{ranges: Range[]} 对象格式）被正确解析为
// LinkedEditRange 列表（Range 字段映射）。
func TestPrepareLinkedEdits_ValidArgs(t *testing.T) {
	svc := newMockLSPServer(t, map[string]func(params interface{}) interface{}{
		"textDocument/prepareLinkedEdits": func(params interface{}) interface{} {
			// LSP/VSCode 响应形如 { ranges: Range[] }（对象，非数组）
			return map[string]interface{}{
				"ranges": []map[string]interface{}{
					{
						"start": map[string]int{"line": 0, "character": 1},
						"end":   map[string]int{"line": 0, "character": 4},
					},
					{
						"start": map[string]int{"line": 0, "character": 10},
						"end":   map[string]int{"line": 0, "character": 13},
					},
				},
			}
		},
	})
	got, err := svc.PrepareLinkedEdits("file:///tmp/ws/main.go", LSPPosition{Line: 0, Character: 1})
	if err != nil {
		t.Fatalf("PrepareLinkedEdits: unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("PrepareLinkedEdits: got %d ranges, want 2", len(got))
	}
	r0 := got[0].Range
	if r0.Start.Line != 0 || r0.Start.Character != 1 || r0.End.Line != 0 || r0.End.Character != 4 {
		t.Errorf("PrepareLinkedEdits ranges[0] = {start:%+v end:%+v}, want start{0,1} end{0,4}", r0.Start, r0.End)
	}
	r1 := got[1].Range
	if r1.Start.Line != 0 || r1.Start.Character != 10 || r1.End.Line != 0 || r1.End.Character != 13 {
		t.Errorf("PrepareLinkedEdits ranges[1] = {start:%+v end:%+v}, want start{0,10} end{0,13}", r1.Start, r1.End)
	}
}

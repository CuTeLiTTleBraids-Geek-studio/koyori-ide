package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type cancelAfterChecksContext struct {
	context.Context
	remaining int
}

func (c *cancelAfterChecksContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func TestCollectSourceFilesStopsOnCancellation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := collectSourceFiles(ctx, dir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("collectSourceFiles error = %v, want context.Canceled", err)
	}
}

func TestSymbolIndex_CancelledLazyIndexRemainsRetryable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := svc.SearchSymbols(ctx, "", 10)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchSymbols error = %v, want context.Canceled", err)
	}
	svc.mu.RLock()
	lastIndex := svc.lastIndex
	indexing := svc.indexing
	svc.mu.RUnlock()
	if !lastIndex.IsZero() || indexing {
		t.Fatalf("cancelled index marked complete: lastIndex=%v indexing=%v", lastIndex, indexing)
	}

	if _, err := svc.SearchSymbols(context.Background(), "", 10); err != nil {
		t.Fatalf("SearchSymbols retry after cancellation: %v", err)
	}
	if svc.GetIndexStats().LastIndex == "" {
		t.Fatal("successful retry did not mark the index complete")
	}
}

func TestSymbolIndex_SearchSymbolsClampsResultLimit(t *testing.T) {
	svc := NewSymbolIndexService()
	svc.symbols = make([]IndexedSymbol, maxSymbolSearchResults+250)
	for i := range svc.symbols {
		svc.symbols[i] = IndexedSymbol{Name: fmt.Sprintf("Symbol%04d", i)}
	}
	svc.lastIndex = time.Now()

	got, err := svc.SearchSymbols(context.Background(), "", maxSymbolSearchResults*10)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(got) != maxSymbolSearchResults {
		t.Fatalf("SearchSymbols returned %d results, want hard cap %d", len(got), maxSymbolSearchResults)
	}
}

func TestSymbolIndex_SearchSymbolsStopsAfterCancellation(t *testing.T) {
	svc := NewSymbolIndexService()
	svc.symbols = make([]IndexedSymbol, 500)
	for i := range svc.symbols {
		svc.symbols[i] = IndexedSymbol{Name: fmt.Sprintf("Symbol%04d", i)}
	}
	svc.lastIndex = time.Now()
	ctx := &cancelAfterChecksContext{Context: context.Background(), remaining: 25}

	got, err := svc.SearchSymbols(ctx, "symbol", 500)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchSymbols error = %v, want context.Canceled", err)
	}
	if len(got) == 0 || len(got) >= 500 {
		t.Fatalf("SearchSymbols returned %d partial results, want cancellation during iteration", len(got))
	}
}

func TestCollectSourceFilesWithBudgetReturnsDeterministicPartialList(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"c.go", "a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package test\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	budget := indexBudget{
		maxFiles:          2,
		maxFileBytes:      1024,
		maxAggregateBytes: 4096,
	}
	files, err := collectSourceFilesWithBudget(context.Background(), dir, budget)
	if !errors.Is(err, ErrSymbolIndexBudgetExceeded) {
		t.Fatalf("collectSourceFilesWithBudget error = %v, want ErrSymbolIndexBudgetExceeded", err)
	}
	want := []string{filepath.Join(dir, "a.go"), filepath.Join(dir, "b.go")}
	if len(files) != len(want) || files[0] != want[0] || files[1] != want[1] {
		t.Fatalf("collectSourceFilesWithBudget partial files = %v, want %v", files, want)
	}
}

func TestSymbolIndex_ReindexBudgetPublishesPartialResults(t *testing.T) {
	const (
		alphaSource = "export const Alpha = 1;\n"
		betaSource  = "export const Beta = 222222222222222222222222222222222222;\n"
		gammaSource = "export const Gamma = 3;\n"
	)

	tests := []struct {
		name               string
		budget             indexBudget
		wantLimit          string
		wantPath           string
		wantValue          int64
		wantMaximum        int64
		wantScannedFiles   int
		wantReadFiles      int
		wantReadBytes      int64
		wantProcessedFiles int
		wantProcessedBytes int64
		wantParsed         []string
		wantSymbols        []string
	}{
		{
			name: "file count",
			budget: indexBudget{
				maxFiles:          1,
				maxFileBytes:      1024,
				maxAggregateBytes: 4096,
			},
			wantLimit:          "maxIndexFiles",
			wantPath:           "b.js",
			wantValue:          2,
			wantMaximum:        1,
			wantScannedFiles:   1,
			wantReadFiles:      1,
			wantReadBytes:      int64(len(alphaSource)),
			wantProcessedFiles: 1,
			wantProcessedBytes: int64(len(alphaSource)),
			wantParsed:         []string{"a.js"},
			wantSymbols:        []string{"Alpha"},
		},
		{
			name: "oversized file is skipped",
			budget: indexBudget{
				maxFiles:          3,
				maxFileBytes:      int64(len(alphaSource)),
				maxAggregateBytes: 4096,
			},
			wantLimit:          "maxIndexFileBytes",
			wantPath:           "b.js",
			wantValue:          int64(len(betaSource)),
			wantMaximum:        int64(len(alphaSource)),
			wantScannedFiles:   3,
			wantReadFiles:      2,
			wantReadBytes:      int64(len(alphaSource) + len(gammaSource)),
			wantProcessedFiles: 2,
			wantProcessedBytes: int64(len(alphaSource) + len(gammaSource)),
			wantParsed:         []string{"a.js", "c.js"},
			wantSymbols:        []string{"Alpha", "Gamma"},
		},
		{
			name: "aggregate bytes",
			budget: indexBudget{
				maxFiles:          3,
				maxFileBytes:      1024,
				maxAggregateBytes: int64(len(alphaSource)),
			},
			wantLimit:          "maxIndexAggregateBytes",
			wantPath:           "b.js",
			wantValue:          int64(len(alphaSource) + len(betaSource)),
			wantMaximum:        int64(len(alphaSource)),
			wantScannedFiles:   2,
			wantReadFiles:      1,
			wantReadBytes:      int64(len(alphaSource)),
			wantProcessedFiles: 1,
			wantProcessedBytes: int64(len(alphaSource)),
			wantParsed:         []string{"a.js"},
			wantSymbols:        []string{"Alpha"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			fileA := filepath.Join(dir, "a.js")
			fileB := filepath.Join(dir, "b.js")
			fileC := filepath.Join(dir, "c.js")
			for _, fixture := range []struct {
				path   string
				source string
			}{
				{path: fileA, source: alphaSource},
				{path: fileB, source: betaSource},
				{path: fileC, source: gammaSource},
			} {
				if err := os.WriteFile(fixture.path, []byte(fixture.source), 0644); err != nil {
					t.Fatal(err)
				}
			}

			svc := NewSymbolIndexService()
			svc.setWorkspaceRoot(dir)
			svc.scanSourceFiles = func(context.Context, string, indexBudget) ([]string, error) {
				return []string{fileC, fileA, fileB}, nil
			}
			originalParse := svc.parseSourceFile
			var parsedFiles []string
			svc.parseSourceFile = func(filePath, root string, content []byte) []IndexedSymbol {
				parsedFiles = append(parsedFiles, filepath.Base(filePath))
				return originalParse(filePath, root, content)
			}

			previousLogger := slog.Default()
			var logs bytes.Buffer
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer slog.SetDefault(previousLogger)

			if err := svc.reindexWithBudget(context.Background(), dir, test.budget); err != nil {
				t.Fatalf("reindexWithBudget: %v", err)
			}
			if strings.Join(parsedFiles, ",") != strings.Join(test.wantParsed, ",") {
				t.Fatalf("parsed files = %v, want %v", parsedFiles, test.wantParsed)
			}
			stats := svc.GetIndexStats()
			if stats.FileCount != test.wantProcessedFiles || stats.SymbolCount != len(test.wantSymbols) {
				t.Fatalf("partial index stats = %+v, want files=%d symbols=%d", stats, test.wantProcessedFiles, len(test.wantSymbols))
			}
			svc.mu.Lock()
			svc.lastIndex = time.Now()
			svc.mu.Unlock()
			symbols, err := svc.SearchSymbols(context.Background(), "", 10)
			if err != nil {
				t.Fatalf("SearchSymbols partial index: %v", err)
			}
			assertSymbolNames(t, symbols, test.wantSymbols...)

			var entry map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry); err != nil {
				t.Fatalf("decode budget warning %q: %v", logs.String(), err)
			}
			if entry["level"] != "WARN" || entry["msg"] != "symbol index budget exceeded; publishing partial results" {
				t.Fatalf("unexpected budget log: %v", entry)
			}
			if entry["root"] != dir || entry["limit"] != test.wantLimit || entry["path"] != filepath.Join(dir, test.wantPath) {
				t.Fatalf("budget log context = %v", entry)
			}
			if entry["reason"] == "" {
				t.Fatalf("budget log has no reason: %v", entry)
			}
			if entry["scannedFiles"] != float64(test.wantScannedFiles) ||
				entry["readFiles"] != float64(test.wantReadFiles) ||
				entry["readBytes"] != float64(test.wantReadBytes) ||
				entry["processedFiles"] != float64(test.wantProcessedFiles) ||
				entry["processedBytes"] != float64(test.wantProcessedBytes) {
				t.Fatalf("budget log usage = %v", entry)
			}
			if entry["value"] != float64(test.wantValue) || entry["maximum"] != float64(test.wantMaximum) {
				t.Fatalf("budget log limit values = %v, want value=%d maximum=%d", entry, test.wantValue, test.wantMaximum)
			}
			if entry["maxIndexFiles"] != float64(test.budget.maxFiles) ||
				entry["maxIndexFileBytes"] != float64(test.budget.maxFileBytes) ||
				entry["maxIndexAggregateBytes"] != float64(test.budget.maxAggregateBytes) {
				t.Fatalf("budget log configured limits = %v", entry)
			}
		})
	}
}

func TestSymbolIndex_ReindexPublishesPartialCollectorResults(t *testing.T) {
	dir := t.TempDir()
	const source = "export const Alpha = 1;\n"
	fileA := filepath.Join(dir, "a.js")
	fileB := filepath.Join(dir, "b.js")
	if err := os.WriteFile(fileA, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)
	svc.scanSourceFiles = func(context.Context, string, indexBudget) ([]string, error) {
		return []string{fileA}, &indexBudgetExceededError{
			limit:   "maxIndexFiles",
			reason:  "test collector file limit",
			path:    fileB,
			value:   2,
			maximum: 1,
		}
	}

	previousLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)

	budget := indexBudget{
		maxFiles:          3,
		maxFileBytes:      1024,
		maxAggregateBytes: 4096,
	}
	if err := svc.reindexWithBudget(context.Background(), dir, budget); err != nil {
		t.Fatalf("reindexWithBudget collector partial result: %v", err)
	}
	svc.mu.RLock()
	symbols := append([]IndexedSymbol(nil), svc.symbols...)
	svc.mu.RUnlock()
	assertSymbolNames(t, symbols, "Alpha")

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry); err != nil {
		t.Fatalf("decode collector budget warning %q: %v", logs.String(), err)
	}
	if entry["root"] != dir || entry["processedFiles"] != float64(1) || entry["processedBytes"] != float64(len(source)) {
		t.Fatalf("collector budget log usage = %v", entry)
	}
	if entry["limit"] != "maxIndexFiles" || entry["path"] != fileB || entry["value"] != float64(2) || entry["maximum"] != float64(1) {
		t.Fatalf("collector budget log context = %v", entry)
	}
}

func TestSymbolIndex_ReindexBudgetAllowsExactLimits(t *testing.T) {
	dir := t.TempDir()
	const source = "export const Exact = 1;\n"
	file := filepath.Join(dir, "exact.js")
	if err := os.WriteFile(file, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)
	svc.scanSourceFiles = func(context.Context, string, indexBudget) ([]string, error) {
		return []string{file}, nil
	}
	previousLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)

	budget := indexBudget{
		maxFiles:          1,
		maxFileBytes:      int64(len(source)),
		maxAggregateBytes: int64(len(source)),
	}
	if err := svc.reindexWithBudget(context.Background(), dir, budget); err != nil {
		t.Fatalf("reindexWithBudget exact limits: %v", err)
	}
	svc.mu.RLock()
	symbols := append([]IndexedSymbol(nil), svc.symbols...)
	svc.mu.RUnlock()
	assertSymbolNames(t, symbols, "Exact")
	if logs.Len() != 0 {
		t.Fatalf("exact budget limits emitted warning: %s", logs.String())
	}
}

func TestSymbolIndex_ReindexSkipsFileAfterBoundedStableSnapshotRetries(t *testing.T) {
	dir := t.TempDir()
	stableFile := filepath.Join(dir, "a.go")
	unstableFile := filepath.Join(dir, "b.go")
	if err := os.WriteFile(stableFile, []byte("package test\n\nfunc Stable() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unstableFile, []byte("package test\n\nfunc Changing() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)
	svc.scanSourceFiles = func(context.Context, string, indexBudget) ([]string, error) {
		return []string{unstableFile, stableFile}, nil
	}
	originalParse := svc.parseSourceFile
	var unstableParses int
	svc.parseSourceFile = func(filePath, root string, content []byte) []IndexedSymbol {
		if filePath == unstableFile {
			unstableParses++
			replacement := fmt.Sprintf(
				"package test\n\nfunc Changing() {}\n// rewrite %s\n",
				strings.Repeat("x", unstableParses),
			)
			if err := os.WriteFile(unstableFile, []byte(replacement), 0644); err != nil {
				t.Fatalf("rewrite unstable source: %v", err)
			}
		}
		return originalParse(filePath, root, content)
	}

	previousLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)

	budget := indexBudget{maxFiles: 2, maxFileBytes: 1024, maxAggregateBytes: 4096}
	if err := svc.reindexWithBudget(context.Background(), dir, budget); err != nil {
		t.Fatalf("reindexWithBudget: %v", err)
	}
	if unstableParses != maxIndexStableAttempts {
		t.Fatalf("unstable file parsed %d times, want bounded retry count %d", unstableParses, maxIndexStableAttempts)
	}
	svc.mu.RLock()
	symbols := append([]IndexedSymbol(nil), svc.symbols...)
	_, unstablePublished := svc.fileHashes[unstableFile]
	svc.mu.RUnlock()
	assertSymbolNames(t, symbols, "Stable")
	if unstablePublished {
		t.Fatal("unstable source file should not be published")
	}
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry); err != nil {
		t.Fatalf("decode unstable source warning %q: %v", logs.String(), err)
	}
	if entry["limit"] != "maxIndexStableAttempts" || entry["path"] != unstableFile {
		t.Fatalf("unstable source warning = %v", entry)
	}
}

func TestSymbolIndex_GoExports(t *testing.T) {
	dir := t.TempDir()
	// Create a go.mod so goPackagePath can resolve.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/test/mod\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Create a Go file with exports.
	goFile := filepath.Join(dir, "pkg", "foo.go")
	if err := os.MkdirAll(filepath.Dir(goFile), 0755); err != nil {
		t.Fatal(err)
	}
	goContent := `package pkg

import "fmt"

// Exported function
func Hello(name string) string {
	return fmt.Sprintf("Hello, %s", name)
}

// Unexported function — should NOT be indexed
func helper() {}

type Config struct {
	Name string
}

type Reader interface {
	Read() error
}

const MaxRetries = 5
var Version = "1.0.0"
`
	if err := os.WriteFile(goFile, []byte(goContent), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)

	syms, err := svc.SearchSymbols(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	want := map[string]bool{"Hello": false, "Config": false, "Reader": false, "MaxRetries": false, "Version": false}
	for _, s := range syms {
		if _, ok := want[s.Name]; ok {
			want[s.Name] = true
		}
		if s.Name == "helper" {
			t.Errorf("unexported function 'helper' should not be indexed")
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("expected symbol %q not found in index (got %d symbols)", name, len(syms))
		}
	}
}

func TestSymbolIndex_JSESExports(t *testing.T) {
	dir := t.TempDir()
	bFile := filepath.Join(dir, "b.js")
	bContent := `// b.js
export default function hello(name) {
	console.log("hello", name);
}

export const PI = 3.14;
export function greet(name) { return "hi " + name; }
export class Animal { constructor(name) { this.name = name; } }
`
	if err := os.WriteFile(bFile, []byte(bContent), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)

	syms, err := svc.SearchSymbols(context.Background(), "hello", 50)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	foundHello := false
	for _, s := range syms {
		if s.Name == "hello" && s.IsDefaultExport {
			foundHello = true
			if s.ExportPath != "./b" {
				t.Errorf("expected ExportPath './b', got %q", s.ExportPath)
			}
		}
	}
	if !foundHello {
		t.Errorf("default export 'hello' not found in index")
	}

	// Also check the other exports are indexed.
	allSyms, _ := svc.SearchSymbols(context.Background(), "", 50)
	want := map[string]bool{"hello": false, "PI": false, "greet": false, "Animal": false}
	for _, s := range allSyms {
		if _, ok := want[s.Name]; ok {
			want[s.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("expected symbol %q not found", name)
		}
	}
}

func TestDefaultExportIdentifier(t *testing.T) {
	tests := []struct {
		filePath string
		want     string
	}{
		{filePath: "alreadyValid.ts", want: "alreadyValid"},
		{filePath: "my-component.tsx", want: "myComponent"},
		{filePath: "123-widget.js", want: "_123Widget"},
		{filePath: "default.js", want: "_default"},
		{filePath: "await.mjs", want: "_await"},
		{filePath: "---.js", want: "defaultExport"},
		{filePath: "组件.ts", want: "defaultExport"},
	}

	for _, test := range tests {
		t.Run(test.filePath, func(t *testing.T) {
			if got := defaultExportIdentifier(test.filePath); got != test.want {
				t.Fatalf("defaultExportIdentifier(%q) = %q, want %q", test.filePath, got, test.want)
			}
		})
	}
}

func TestParseESExportLine_DefaultExportNames(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		line     string
		wantName string
		wantKind int
	}{
		{
			name:     "anonymous function",
			filePath: "my-component.js",
			line:     "export default function() {}",
			wantName: "myComponent",
			wantKind: SymbolKindFunction,
		},
		{
			name:     "anonymous generator",
			filePath: "event-stream.js",
			line:     "export default function* () {}",
			wantName: "eventStream",
			wantKind: SymbolKindFunction,
		},
		{
			name:     "anonymous class",
			filePath: "123-widget.ts",
			line:     "export default class{}",
			wantName: "_123Widget",
			wantKind: SymbolKindClass,
		},
		{
			name:     "default expression reserved basename",
			filePath: "default.js",
			line:     "export default { enabled: true }",
			wantName: "_default",
			wantKind: SymbolKindVariable,
		},
		{
			name:     "explicit ASCII name is unchanged",
			filePath: "ignored-name.js",
			line:     "export default function ExplicitName() {}",
			wantName: "ExplicitName",
			wantKind: SymbolKindFunction,
		},
		{
			name:     "explicit Unicode name is unchanged",
			filePath: "ignored-name.js",
			line:     "export default class 组件 {}",
			wantName: "组件",
			wantKind: SymbolKindClass,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			symbols := parseESExportLine(test.line, "./module", test.filePath, 1, true)
			if len(symbols) != 1 {
				t.Fatalf("parseESExportLine returned %d symbols: %+v", len(symbols), symbols)
			}
			symbol := symbols[0]
			if symbol.Name != test.wantName || symbol.Kind != test.wantKind || !symbol.IsDefaultExport {
				t.Fatalf("default symbol = %+v, want name=%q kind=%d", symbol, test.wantName, test.wantKind)
			}
		})
	}
}

func TestSymbolIndex_DefaultExportFilenamePrefixSearch(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "my-component.js")
	if err := os.WriteFile(filePath, []byte("export default function() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)
	symbols, err := svc.SearchSymbols(context.Background(), "myC", 20)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(symbols) != 1 || symbols[0].Name != "myComponent" || !symbols[0].IsDefaultExport {
		t.Fatalf("prefix search returned %+v, want myComponent default export", symbols)
	}
}

func TestSymbolIndex_JSCJSExports(t *testing.T) {
	dir := t.TempDir()
	cjsFile := filepath.Join(dir, "utils.js")
	cjsContent := `module.exports.Foo = function() { return 42; };
module.exports.bar = "hello";
exports.baz = { key: "value" };
`
	if err := os.WriteFile(cjsFile, []byte(cjsContent), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)

	syms, err := svc.SearchSymbols(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	want := map[string]bool{"Foo": false, "bar": false, "baz": false}
	for _, s := range syms {
		if _, ok := want[s.Name]; ok {
			want[s.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("CJS export %q not found in index", name)
		}
	}
}

func TestSymbolIndex_CJSDefaultExportNames(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		fileName string
		content  string
		wantName string
		wantKind int
	}{
		{
			fileName: "my-component.js",
			content:  "module.exports=function() {};\n",
			wantName: "myComponent",
			wantKind: SymbolKindFunction,
		},
		{
			fileName: "123-widget.js",
			content:  "module.exports = function () {};\n",
			wantName: "_123Widget",
			wantKind: SymbolKindFunction,
		},
		{
			fileName: "default.js",
			content:  "module.exports = function* () {};\n",
			wantName: "_default",
			wantKind: SymbolKindFunction,
		},
		{
			fileName: "widget-class.js",
			content:  "module.exports=class {};\n",
			wantName: "widgetClass",
			wantKind: SymbolKindClass,
		},
		{
			fileName: "settings-map.js",
			content:  "module.exports = { enabled: true };\n",
			wantName: "settingsMap",
			wantKind: SymbolKindVariable,
		},
		{
			fileName: "named.js",
			content:  "module.exports = function ExplicitName() {};\n",
			wantName: "ExplicitName",
			wantKind: SymbolKindFunction,
		},
	}

	for _, test := range tests {
		t.Run(test.fileName, func(t *testing.T) {
			filePath := filepath.Join(dir, test.fileName)
			if err := os.WriteFile(filePath, []byte(test.content), 0644); err != nil {
				t.Fatal(err)
			}
			symbols := parseCJSExports(filePath, dir)
			if len(symbols) != 1 {
				t.Fatalf("parseCJSExports returned %d symbols: %+v", len(symbols), symbols)
			}
			symbol := symbols[0]
			if symbol.Name != test.wantName || symbol.Kind != test.wantKind || !symbol.IsDefaultExport {
				t.Fatalf("CJS default symbol = %+v, want name=%q kind=%d", symbol, test.wantName, test.wantKind)
			}
			if symbol.Name == "default" {
				t.Fatal("CJS default export must never use reserved word default")
			}
		})
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)
	symbols, err := svc.SearchSymbols(context.Background(), "myC", 20)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(symbols) != 1 || symbols[0].Name != "myComponent" || !symbols[0].IsDefaultExport {
		t.Fatalf("CJS prefix search returned %+v, want myComponent default export", symbols)
	}
}

func TestSymbolIndex_TSExports(t *testing.T) {
	dir := t.TempDir()
	tsFile := filepath.Join(dir, "types.ts")
	tsContent := `export interface User { id: number; name: string; }
export type Status = "active" | "inactive";
export enum Color { Red, Green, Blue }
export const DEFAULT_PAGE_SIZE = 10;
export function getUser(id: number): User { return { id, name: "" }; }
`
	if err := os.WriteFile(tsFile, []byte(tsContent), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)

	syms, err := svc.SearchSymbols(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	want := map[string]bool{
		"User": false, "Status": false, "Color": false,
		"DEFAULT_PAGE_SIZE": false, "getUser": false,
	}
	for _, s := range syms {
		if _, ok := want[s.Name]; ok {
			want[s.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("TS export %q not found in index (got %d symbols)", name, len(syms))
		}
	}
}

func TestSymbolIndex_GetAutoImportCandidates(t *testing.T) {
	dir := t.TempDir()
	bFile := filepath.Join(dir, "b.js")
	aFile := filepath.Join(dir, "a.js")
	// `export default hello;` — the default export is named after the file ("b"),
	// matching how TypeScript auto-import works: type "b" → see b.js's default export.
	if err := os.WriteFile(bFile, []byte("export default hello;\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aFile, []byte("// a.js\nconsole.log('a');\n"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)

	// Typing "b" (the file basename) should surface the default export from b.js.
	cands, err := svc.GetAutoImportCandidates(context.Background(), "b", aFile)
	if err != nil {
		t.Fatalf("GetAutoImportCandidates: %v", err)
	}
	if len(cands) == 0 {
		t.Fatal("expected at least one auto-import candidate for 'b'")
	}
	c := cands[0]
	if c.Name != "b" {
		t.Errorf("expected candidate name 'b' (file basename for default export), got %q", c.Name)
	}
	if !c.IsDefaultExport {
		t.Errorf("expected default export")
	}
	if c.ExportPath != "./b" {
		t.Errorf("expected export path './b', got %q", c.ExportPath)
	}
	if c.FilePath == aFile {
		t.Errorf("candidate from same file should be excluded")
	}
}

func TestSymbolIndex_IncrementalUpdate(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goFile, []byte("package main\n\nfunc Foo() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)

	syms, _ := svc.SearchSymbols(context.Background(), "Foo", 50)
	if len(syms) != 1 {
		t.Fatalf("expected 1 symbol, got %d", len(syms))
	}

	// Force the file mtime to change by sleeping briefly before re-writing.
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(goFile, []byte("package main\n\nfunc Foo() {}\nfunc Bar() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Force re-index by clearing the lastIndex (simulating debounce expiry).
	svc.mu.Lock()
	svc.lastIndex = svc.lastIndex.Add(-time.Hour) // 1 hour ago — well past 5s debounce
	// Clear cached mtime so the file is re-parsed.
	for k := range svc.fileMTimes {
		delete(svc.fileMTimes, k)
	}
	svc.mu.Unlock()

	syms, _ = svc.SearchSymbols(context.Background(), "", 50)
	names := make(map[string]bool)
	for _, s := range syms {
		names[s.Name] = true
	}
	if !names["Foo"] || !names["Bar"] {
		t.Errorf("expected Foo and Bar in index after update, got %v", names)
	}
}

func TestSymbolIndex_B10IndexFileUpdatesOnlyOneFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	fileA := filepath.Join(dir, "a.go")
	fileB := filepath.Join(dir, "b.go")
	if err := os.WriteFile(fileA, []byte("package test\n\nfunc Alpha() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("package test\n\nfunc Beta() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)
	initial, err := svc.SearchSymbols(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("initial SearchSymbols: %v", err)
	}
	assertSymbolNames(t, initial, "Alpha", "Beta")

	svc.mu.RLock()
	initialVersion := svc.indexVersion
	oldAHash := svc.fileHashes[fileA]
	oldBHash := svc.fileHashes[fileB]
	svc.mu.RUnlock()
	if oldAHash == "" || oldBHash == "" {
		t.Fatalf("initial file hashes were not populated: a=%q b=%q", oldAHash, oldBHash)
	}

	var scans atomic.Int32
	svc.mu.Lock()
	svc.scanSourceFiles = func(ctx context.Context, root string, budget indexBudget) ([]string, error) {
		scans.Add(1)
		return collectSourceFilesWithBudget(ctx, root, budget)
	}
	svc.mu.Unlock()

	updatedA := []byte("package test\n\nfunc Gamma() {}\n")
	if err := os.WriteFile(fileA, updatedA, 0644); err != nil {
		t.Fatal(err)
	}
	if err := svc.IndexFile(fileA); err != nil {
		t.Fatalf("IndexFile changed file: %v", err)
	}
	updated, err := svc.SearchSymbols(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("SearchSymbols after IndexFile: %v", err)
	}
	assertSymbolNames(t, updated, "Gamma", "Beta")

	svc.mu.RLock()
	updatedVersion := svc.indexVersion
	newAHash := svc.fileHashes[fileA]
	newBHash := svc.fileHashes[fileB]
	svc.mu.RUnlock()
	if updatedVersion != initialVersion+1 {
		t.Fatalf("index version = %d, want %d", updatedVersion, initialVersion+1)
	}
	if newAHash == oldAHash || newAHash == "" {
		t.Fatalf("changed file hash was not updated: old=%q new=%q", oldAHash, newAHash)
	}
	if newBHash != oldBHash {
		t.Fatalf("unchanged file hash changed: old=%q new=%q", oldBHash, newBHash)
	}
	if scans.Load() != 0 {
		t.Fatalf("IndexFile triggered %d workspace scans", scans.Load())
	}

	// Rewriting identical content may change metadata, but must not publish a
	// new symbol snapshot because the content hash is unchanged.
	if err := os.WriteFile(fileA, updatedA, 0644); err != nil {
		t.Fatal(err)
	}
	if err := svc.IndexFile(fileA); err != nil {
		t.Fatalf("IndexFile unchanged content: %v", err)
	}
	if got := svc.GetIndexStats().IndexVersion; got != updatedVersion {
		t.Fatalf("unchanged content bumped index version to %d, want %d", got, updatedVersion)
	}

	fileC := filepath.Join(dir, "c.go")
	if err := os.WriteFile(fileC, []byte("package test\n\nfunc Delta() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := svc.IndexFile(fileC); err != nil {
		t.Fatalf("IndexFile new file: %v", err)
	}
	withNewFile, err := svc.SearchSymbols(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("SearchSymbols after adding file: %v", err)
	}
	assertSymbolNames(t, withNewFile, "Gamma", "Beta", "Delta")

	if err := os.Remove(fileA); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveFile(fileA); err != nil {
		t.Fatalf("RemoveFile: %v", err)
	}
	afterRemove, err := svc.SearchSymbols(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("SearchSymbols after RemoveFile: %v", err)
	}
	assertSymbolNames(t, afterRemove, "Beta", "Delta")
	svc.mu.RLock()
	_, hasRemovedHash := svc.fileHashes[fileA]
	_, hasOtherHash := svc.fileHashes[fileB]
	svc.mu.RUnlock()
	if hasRemovedHash || !hasOtherHash {
		t.Fatalf("unexpected hash state after removal: removed=%v other=%v", hasRemovedHash, hasOtherHash)
	}
	versionAfterRemove := svc.GetIndexStats().IndexVersion
	if err := svc.RemoveFile(fileA); err != nil {
		t.Fatalf("second RemoveFile: %v", err)
	}
	if got := svc.GetIndexStats().IndexVersion; got != versionAfterRemove {
		t.Fatalf("idempotent RemoveFile bumped version to %d, want %d", got, versionAfterRemove)
	}
	if scans.Load() != 0 {
		t.Fatalf("incremental operations triggered %d workspace scans", scans.Load())
	}
}

func TestSymbolIndex_B10ConcurrentFileUpdates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	fileA := filepath.Join(dir, "a.go")
	fileB := filepath.Join(dir, "b.go")
	if err := os.WriteFile(fileA, []byte("package test\n\nfunc OldA() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("package test\n\nfunc OldB() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)
	if _, err := svc.SearchSymbols(context.Background(), "", 50); err != nil {
		t.Fatalf("initial SearchSymbols: %v", err)
	}
	if err := os.WriteFile(fileA, []byte("package test\n\nfunc NewA() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("package test\n\nfunc NewB() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, filePath := range []string{fileA, fileB} {
		filePath := filePath
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- svc.IndexFile(filePath)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent IndexFile: %v", err)
		}
	}

	symbols, err := svc.SearchSymbols(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("SearchSymbols after concurrent updates: %v", err)
	}
	assertSymbolNames(t, symbols, "NewA", "NewB")
}

func TestSymbolIndex_B10RemoveInvalidatesInFlightIndexFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "ghost.go")
	if err := os.WriteFile(file, []byte("package test\n\nfunc Ghost() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)
	parseStarted := make(chan struct{})
	releaseParse := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseParse)
		}
	}()
	originalParse := svc.parseSourceFile
	svc.mu.Lock()
	svc.parseSourceFile = func(filePath, root string, content []byte) []IndexedSymbol {
		close(parseStarted)
		<-releaseParse
		return originalParse(filePath, root, content)
	}
	versionBeforeRemove := svc.indexVersion
	svc.mu.Unlock()

	indexDone := make(chan error, 1)
	go func() {
		indexDone <- svc.IndexFile(file)
	}()
	select {
	case <-parseStarted:
	case <-time.After(time.Second):
		t.Fatal("IndexFile did not reach the parse barrier")
	}

	if err := svc.RemoveFile(file); err != nil {
		t.Fatalf("RemoveFile during IndexFile: %v", err)
	}
	if got := svc.GetIndexStats().IndexVersion; got != versionBeforeRemove {
		t.Fatalf("removing an unindexed file bumped index version to %d, want %d", got, versionBeforeRemove)
	}
	close(releaseParse)
	released = true
	select {
	case err := <-indexDone:
		if err != nil {
			t.Fatalf("IndexFile after RemoveFile: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("IndexFile did not finish after releasing the parse barrier")
	}

	svc.mu.RLock()
	symbols := append([]IndexedSymbol(nil), svc.symbols...)
	_, hasHash := svc.fileHashes[file]
	_, hasMTime := svc.fileMTimes[file]
	svc.mu.RUnlock()
	if len(symbols) != 0 || hasHash || hasMTime {
		t.Fatalf("removed file was published by stale IndexFile: symbols=%+v hash=%v mtime=%v", symbols, hasHash, hasMTime)
	}
}

func TestSymbolIndex_B10RemoveInvalidatesInFlightReindex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "ghost.go")
	if err := os.WriteFile(file, []byte("package test\n\nfunc Ghost() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)
	parseStarted := make(chan struct{})
	releaseParse := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseParse)
		}
	}()
	originalParse := svc.parseSourceFile
	svc.mu.Lock()
	svc.scanSourceFiles = func(context.Context, string, indexBudget) ([]string, error) {
		return []string{file}, nil
	}
	svc.parseSourceFile = func(filePath, root string, content []byte) []IndexedSymbol {
		close(parseStarted)
		<-releaseParse
		return originalParse(filePath, root, content)
	}
	versionBeforeRemove := svc.indexVersion
	svc.mu.Unlock()

	reindexDone := make(chan error, 1)
	go func() {
		reindexDone <- svc.reindex(context.Background(), dir)
	}()
	select {
	case <-parseStarted:
	case <-time.After(time.Second):
		t.Fatal("reindex did not reach the parse barrier")
	}

	if err := svc.RemoveFile(file); err != nil {
		t.Fatalf("RemoveFile during reindex: %v", err)
	}
	if got := svc.GetIndexStats().IndexVersion; got != versionBeforeRemove {
		t.Fatalf("removing an unindexed file bumped index version to %d, want %d", got, versionBeforeRemove)
	}
	close(releaseParse)
	released = true
	select {
	case err := <-reindexDone:
		if err != nil {
			t.Fatalf("reindex after RemoveFile: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reindex did not finish after releasing the parse barrier")
	}

	svc.mu.RLock()
	symbols := append([]IndexedSymbol(nil), svc.symbols...)
	_, hasHash := svc.fileHashes[file]
	_, hasMTime := svc.fileMTimes[file]
	svc.mu.RUnlock()
	if len(symbols) != 0 || hasHash || hasMTime {
		t.Fatalf("removed file was published by stale reindex: symbols=%+v hash=%v mtime=%v", symbols, hasHash, hasMTime)
	}
}

func TestSymbolIndex_B10ReindexRetriesConcurrentRewrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "changing.go")
	if err := os.WriteFile(file, []byte("package test\n\nfunc OldSymbol() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)
	parseStarted := make(chan struct{})
	releaseParse := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseParse)
		}
	}()
	originalParse := svc.parseSourceFile
	var parseCalls atomic.Int32
	svc.mu.Lock()
	svc.scanSourceFiles = func(context.Context, string, indexBudget) ([]string, error) {
		return []string{file}, nil
	}
	svc.parseSourceFile = func(filePath, root string, content []byte) []IndexedSymbol {
		if parseCalls.Add(1) == 1 {
			close(parseStarted)
			<-releaseParse
		}
		return originalParse(filePath, root, content)
	}
	svc.mu.Unlock()

	reindexDone := make(chan error, 1)
	go func() {
		reindexDone <- svc.reindex(context.Background(), dir)
	}()
	select {
	case <-parseStarted:
	case <-time.After(time.Second):
		t.Fatal("reindex did not reach the parse barrier")
	}

	if err := os.WriteFile(file, []byte("package test\n\nfunc NewSymbol() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	close(releaseParse)
	released = true
	select {
	case err := <-reindexDone:
		if err != nil {
			t.Fatalf("reindex after concurrent rewrite: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reindex did not finish after releasing the parse barrier")
	}
	if got := parseCalls.Load(); got < 2 {
		t.Fatalf("reindex parsed %d time(s), want a retry after the content hash changed", got)
	}

	wantHash, _, err := sourceFileMetadata(file)
	if err != nil {
		t.Fatalf("sourceFileMetadata: %v", err)
	}
	svc.mu.RLock()
	symbols := append([]IndexedSymbol(nil), svc.symbols...)
	gotHash := svc.fileHashes[file]
	svc.mu.RUnlock()
	assertSymbolNames(t, symbols, "NewSymbol")
	if gotHash != wantHash {
		t.Fatalf("cached hash = %q, want current content hash %q", gotHash, wantHash)
	}
}

func TestSymbolIndex_ReindexDoesNotBlockQueriesOrPublishStaleSnapshot(t *testing.T) {
	dir := t.TempDir()
	oldFile := filepath.Join(dir, "old.go")
	newFile := filepath.Join(dir, "new.go")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldFile, []byte("package test\n\nfunc OldOne() {}\nfunc OldTwo() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)
	initial, err := svc.SearchSymbols(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("initial SearchSymbols: %v", err)
	}
	assertSymbolNames(t, initial, "OldOne", "OldTwo")

	if err := os.WriteFile(newFile, []byte("package test\n\nfunc NewOne() {}\nfunc NewTwo() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	olderScanStarted := make(chan struct{})
	releaseOlderScan := make(chan struct{})
	releasedOlderScan := false
	defer func() {
		if !releasedOlderScan {
			close(releaseOlderScan)
		}
	}()
	var scanCalls atomic.Int32
	svc.scanSourceFiles = func(context.Context, string, indexBudget) ([]string, error) {
		if scanCalls.Add(1) == 1 {
			close(olderScanStarted)
			<-releaseOlderScan
			return []string{oldFile}, nil
		}
		return []string{newFile}, nil
	}

	type searchResult struct {
		symbols []IndexedSymbol
		err     error
	}
	svc.mu.Lock()
	svc.lastIndex = time.Time{}
	svc.mu.Unlock()
	olderDone := make(chan searchResult, 1)
	go func() {
		syms, searchErr := svc.SearchSymbols(context.Background(), "", 50)
		olderDone <- searchResult{symbols: syms, err: searchErr}
	}()
	select {
	case <-olderScanStarted:
	case <-time.After(time.Second):
		t.Fatal("lazy reindex did not reach the scan barrier")
	}

	queryDone := make(chan searchResult, 1)
	go func() {
		syms, searchErr := svc.SearchSymbols(context.Background(), "", 50)
		queryDone <- searchResult{symbols: syms, err: searchErr}
	}()
	select {
	case result := <-queryDone:
		if result.err != nil {
			t.Fatalf("SearchSymbols during scan: %v", result.err)
		}
		assertSymbolNames(t, result.symbols, "OldOne", "OldTwo")
	case <-time.After(time.Second):
		t.Fatal("SearchSymbols blocked while reindex was scanning")
	}

	if err := svc.reindex(context.Background(), dir); err != nil {
		t.Fatalf("newer reindex: %v", err)
	}
	newer, err := svc.SearchSymbols(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("SearchSymbols after newer reindex: %v", err)
	}
	assertSymbolNames(t, newer, "NewOne", "NewTwo")

	close(releaseOlderScan)
	releasedOlderScan = true
	var older searchResult
	select {
	case older = <-olderDone:
	case <-time.After(time.Second):
		t.Fatal("older SearchSymbols did not finish after releasing the scan barrier")
	}
	if older.err != nil {
		t.Fatalf("older SearchSymbols: %v", older.err)
	}
	assertSymbolNames(t, older.symbols, "NewOne", "NewTwo")
}

func assertSymbolNames(t *testing.T, symbols []IndexedSymbol, want ...string) {
	t.Helper()
	if len(symbols) != len(want) {
		t.Fatalf("symbol count = %d, want %d: %+v", len(symbols), len(want), symbols)
	}
	got := make(map[string]bool, len(symbols))
	for _, symbol := range symbols {
		got[symbol.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("symbol %q not found in %+v", name, symbols)
		}
	}
}

// --- Architecture improvement E: AST-based Go symbol parsing ---

// writeGoASTFixture writes a go.mod and a Go source file under dir/astpkg and
// returns the file path. It is a helper for the ArchE tests.
func writeGoASTFixture(t *testing.T, dir, src string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/test/astmod\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	goFile := filepath.Join(dir, "astpkg", "foo.go")
	if err := os.MkdirAll(filepath.Dir(goFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goFile, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	return goFile
}

func TestSymbolIndex_ArchE_GoAST_FunctionDecl(t *testing.T) {
	dir := t.TempDir()
	src := `package astpkg

func Hello(name string) string {
	return "hi " + name
}

func helper() {}
`
	goFile := writeGoASTFixture(t, dir, src)

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)

	syms, err := svc.parseGoFileAST(goFile)
	if err != nil {
		t.Fatalf("parseGoFileAST: %v", err)
	}
	var hello *IndexedSymbol
	for i := range syms {
		if syms[i].Name == "Hello" {
			hello = &syms[i]
		}
		if syms[i].Name == "helper" {
			t.Errorf("unexported helper must not be indexed via AST")
		}
	}
	if hello == nil {
		t.Fatalf("Hello not found; got %d symbols: %+v", len(syms), syms)
	}
	if hello.Kind != SymbolKindFunction {
		t.Errorf("Hello kind = %d, want %d (Function)", hello.Kind, SymbolKindFunction)
	}
	// Hello is declared on line 3 (1-indexed); the index stores 0-indexed lines.
	if hello.Line != 2 {
		t.Errorf("Hello line = %d, want 2 (0-indexed line 3)", hello.Line)
	}
	if !strings.Contains(hello.Detail, "func") || !strings.Contains(hello.Detail, "Hello") {
		t.Errorf("Hello detail = %q, want a func signature", hello.Detail)
	}
	if !strings.Contains(hello.Detail, "name string") {
		t.Errorf("Hello detail = %q, want params to include 'name string'", hello.Detail)
	}
	if !strings.Contains(hello.Detail, "string") {
		t.Errorf("Hello detail = %q, want result type string", hello.Detail)
	}
	if hello.ExportPath != "github.com/test/astmod/astpkg" {
		t.Errorf("Hello exportPath = %q, want github.com/test/astmod/astpkg", hello.ExportPath)
	}
}

func TestSymbolIndex_ArchE_GoAST_MethodDecl(t *testing.T) {
	dir := t.TempDir()
	src := `package astpkg

type MyType struct {
	Name string
}

func (m *MyType) Greet(name string) string {
	return m.Name + name
}
`
	goFile := writeGoASTFixture(t, dir, src)

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)

	syms, err := svc.parseGoFileAST(goFile)
	if err != nil {
		t.Fatalf("parseGoFileAST: %v", err)
	}
	var greet *IndexedSymbol
	for i := range syms {
		if syms[i].Name == "Greet" {
			greet = &syms[i]
		}
	}
	if greet == nil {
		t.Fatalf("Greet not found; got %+v", syms)
	}
	if greet.Kind != SymbolKindMethod {
		t.Errorf("Greet kind = %d, want %d (Method)", greet.Kind, SymbolKindMethod)
	}
	// Receiver type must be captured in the detail string.
	if !strings.Contains(greet.Detail, "MyType") {
		t.Errorf("Greet detail = %q, want receiver type MyType captured", greet.Detail)
	}
	if !strings.Contains(greet.Detail, "*MyType") {
		t.Errorf("Greet detail = %q, want pointer receiver *MyType", greet.Detail)
	}
	// Greet is on line 7 (1-indexed); stored Line = 6.
	if greet.Line != 6 {
		t.Errorf("Greet line = %d, want 6", greet.Line)
	}
}

func TestSymbolIndex_ArchE_GoAST_StructDecl(t *testing.T) {
	dir := t.TempDir()
	src := `package astpkg

type Config struct {
	Name  string
	Value int
}
`
	goFile := writeGoASTFixture(t, dir, src)

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)

	syms, err := svc.parseGoFileAST(goFile)
	if err != nil {
		t.Fatalf("parseGoFileAST: %v", err)
	}
	var cfg *IndexedSymbol
	for i := range syms {
		if syms[i].Name == "Config" {
			cfg = &syms[i]
		}
	}
	if cfg == nil {
		t.Fatalf("Config not found; got %+v", syms)
	}
	// Structs map to SymbolKindClass (matches the regex scanner and LSP Go).
	if cfg.Kind != SymbolKindClass {
		t.Errorf("Config kind = %d, want %d (Class, used for structs)", cfg.Kind, SymbolKindClass)
	}
	if !strings.Contains(cfg.Detail, "struct") {
		t.Errorf("Config detail = %q, want it to indicate struct", cfg.Detail)
	}
	if !strings.Contains(cfg.Detail, "2") {
		t.Errorf("Config detail = %q, want field count 2", cfg.Detail)
	}
	// Config on line 3 (1-indexed); stored Line = 2.
	if cfg.Line != 2 {
		t.Errorf("Config line = %d, want 2", cfg.Line)
	}
}

func TestSymbolIndex_ArchE_GoAST_InterfaceDecl(t *testing.T) {
	dir := t.TempDir()
	src := `package astpkg

type Reader interface {
	Read() error
	Close() error
}
`
	goFile := writeGoASTFixture(t, dir, src)

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)

	syms, err := svc.parseGoFileAST(goFile)
	if err != nil {
		t.Fatalf("parseGoFileAST: %v", err)
	}
	var reader *IndexedSymbol
	for i := range syms {
		if syms[i].Name == "Reader" {
			reader = &syms[i]
		}
	}
	if reader == nil {
		t.Fatalf("Reader not found; got %+v", syms)
	}
	if reader.Kind != SymbolKindInterface {
		t.Errorf("Reader kind = %d, want %d (Interface)", reader.Kind, SymbolKindInterface)
	}
	if !strings.Contains(reader.Detail, "interface") {
		t.Errorf("Reader detail = %q, want it to indicate interface", reader.Detail)
	}
	if !strings.Contains(reader.Detail, "2") {
		t.Errorf("Reader detail = %q, want method count 2", reader.Detail)
	}
}

func TestSymbolIndex_ArchE_GoAST_ConstAndVar(t *testing.T) {
	dir := t.TempDir()
	// Grouped declarations — a case the regex scanner explicitly skips.
	// AST parsing must surface every exported name in the groups.
	src := `package astpkg

const (
	MaxRetries = 5
	Timeout    = 30
)

var (
	Version = "1.0.0"
	Debug   = true
)

const Pi = 3.14
var Name = "koyori-ide"
`
	goFile := writeGoASTFixture(t, dir, src)

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)

	syms, err := svc.parseGoFileAST(goFile)
	if err != nil {
		t.Fatalf("parseGoFileAST: %v", err)
	}
	want := map[string]int{
		"MaxRetries": SymbolKindConstant,
		"Timeout":    SymbolKindConstant,
		"Version":    SymbolKindVariable,
		"Debug":      SymbolKindVariable,
		"Pi":         SymbolKindConstant,
		"Name":       SymbolKindVariable,
	}
	got := map[string]IndexedSymbol{}
	for _, s := range syms {
		got[s.Name] = s
	}
	for name, kind := range want {
		s, ok := got[name]
		if !ok {
			t.Errorf("expected symbol %q not found (got %d symbols: %+v)", name, len(syms), syms)
			continue
		}
		if s.Kind != kind {
			t.Errorf("%q kind = %d, want %d", name, s.Kind, kind)
		}
	}
	// Sanity: grouped const/var must be indexed (regex path would miss them).
	if len(syms) < len(want) {
		t.Errorf("expected at least %d symbols from grouped decls, got %d", len(want), len(syms))
	}
}

func TestSymbolIndex_ArchE_GoAST_NonGoFallback(t *testing.T) {
	dir := t.TempDir()
	jsFile := filepath.Join(dir, "b.js")
	jsContent := `export const Greeting = "hi";
export function sayHi(name) { return "hi " + name; }
`
	if err := os.WriteFile(jsFile, []byte(jsContent), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)

	// Non-Go files must still be indexed via the regex scanner.
	syms, err := svc.SearchSymbols(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	want := map[string]bool{"Greeting": false, "sayHi": false}
	for _, s := range syms {
		if _, ok := want[s.Name]; ok {
			want[s.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("expected JS symbol %q not found (got %d symbols)", name, len(syms))
		}
	}

	// parseFileExportsWithAST on a non-Go file must delegate to the regex
	// scanner (not the Go AST path).
	out := svc.parseFileExportsWithAST(jsFile)
	if len(out) == 0 {
		t.Errorf("expected regex-based symbols for JS file, got none")
	}
	foundGreeting := false
	for _, s := range out {
		if s.Name == "Greeting" {
			foundGreeting = true
		}
	}
	if !foundGreeting {
		t.Errorf("regex fallback for JS file did not surface Greeting")
	}
}

func TestSymbolIndex_ArchE_GoAST_SyntaxError(t *testing.T) {
	dir := t.TempDir()
	// Syntactically invalid Go: missing closing paren on Bad.
	src := `package astpkg

func Good() string {
	return "ok"
}

func Bad( {
}
`
	goFile := writeGoASTFixture(t, dir, src)

	svc := NewSymbolIndexService()
	svc.setWorkspaceRoot(dir)

	// Direct AST parse must return a graceful error (no panic).
	syms, err := svc.parseGoFileAST(goFile)
	if err == nil {
		t.Errorf("expected parse error for syntactically invalid Go file, got nil (syms=%+v)", syms)
	}

	// The indexing pipeline must not crash; it should fall back to regex and
	// still surface the well-formed declarations.
	pipelineSyms, err := svc.SearchSymbols(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("SearchSymbols on syntax-error file: %v", err)
	}
	foundGood := false
	for _, s := range pipelineSyms {
		if s.Name == "Good" {
			foundGood = true
		}
	}
	if !foundGood {
		t.Errorf("expected regex fallback to surface 'Good'; got %d symbols: %+v", len(pipelineSyms), pipelineSyms)
	}
}

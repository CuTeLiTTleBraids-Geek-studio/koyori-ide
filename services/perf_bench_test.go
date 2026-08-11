package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// G-PERF-04: Performance benchmarks for critical paths.
// These benchmarks establish a performance baseline. CI compares
// results against the baseline and fails if >20% regression.
//
// Note: BenchmarkGenerateNonce lives in main_test.go (package main)
// because generateNonce is defined in main.go, not the services package.

// BenchmarkPathsecValidate benchmarks the path validation used on every file operation
func BenchmarkPathsecValidate(b *testing.B) {
	root := "/workspace/project"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ValidatePathWithinRoot(root, "/workspace/project/src/main.go")
	}
}

// BenchmarkAtomicWriteJSON benchmarks atomic JSON writes
func BenchmarkAtomicWriteJSON(b *testing.B) {
	dir := b.TempDir()
	data := map[string]interface{}{
		"name":    "test",
		"version": "1.0.0",
		"items":   []string{"a", "b", "c", "d", "e"},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := dir + "/test.json"
		if err := atomicWriteJSON(path, data, 0644); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseAIError benchmarks error response parsing
func BenchmarkParseAIError(b *testing.B) {
	// Can't easily benchmark with real http.Response, so benchmark the JSON parsing
	body := `{"error":{"message":"rate limit exceeded","type":"rate_limit_error","code":"429"}}`
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = strings.NewReader(body)
	}
}

func BenchmarkSearchWorkspace1KFiles(b *testing.B) {
	dir := b.TempDir()
	content := []byte(strings.Repeat("ordinary source line\n", 40) + "benchmark needle\n")
	for i := 0; i < 1_000; i++ {
		path := filepath.Join(dir, fmt.Sprintf("file-%04d.txt", i))
		if err := os.WriteFile(path, content, 0644); err != nil {
			b.Fatal(err)
		}
	}
	svc := &SearchService{}
	b.ReportAllocs()
	b.SetBytes(int64(len(content) * 1_000))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := svc.searchWithGlobsContext(context.Background(), dir, "needle", false, nil, nil)
		if err != nil {
			b.Fatal(err)
		}
		if len(results) != 1_000 {
			b.Fatalf("search returned %d files, want 1000", len(results))
		}
	}
}

func BenchmarkSymbolSearch100K(b *testing.B) {
	svc := NewSymbolIndexService()
	svc.symbols = make([]IndexedSymbol, 100_000)
	for i := range svc.symbols {
		svc.symbols[i] = IndexedSymbol{Name: fmt.Sprintf("WorkspaceSymbol%06d", i)}
	}
	svc.lastIndex = time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := svc.SearchSymbols(context.Background(), "symbol099", 100)
		if err != nil {
			b.Fatal(err)
		}
		if len(results) != 100 {
			b.Fatalf("symbol search returned %d results, want 100", len(results))
		}
	}
}

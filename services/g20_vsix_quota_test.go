package services

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildVSIXZip builds an in-memory VSIX-like zip with the given entries.
func buildVSIXZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func extractVSIXBytes(t *testing.T, data []byte) error {
	t.Helper()
	dir := t.TempDir()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip reader: %v", err)
	}
	return extractVSIXEntries(zr, dir)
}

func TestG20_VSIX_NormalExtractionSucceeds(t *testing.T) {
	data := buildVSIXZip(t, map[string][]byte{
		"extension/package.json": []byte(`{"name":"x","version":"1.0.0","engines":{"vscode":"^1.70.0"}}`),
		"extension/out/main.js":  []byte("console.log('ok');"),
	})
	if err := extractVSIXBytes(t, data); err != nil {
		t.Fatalf("normal VSIX rejected: %v", err)
	}
}

func TestG20_VSIX_ZipBombCompressionRatioRejected(t *testing.T) {
	// Highly compressible data that declares a huge uncompressed size.
	big := bytes.Repeat([]byte("A"), 20<<20) // 20 MiB of zeros-ish
	data := buildVSIXZip(t, map[string][]byte{
		"extension/out/bomb.bin": big,
	})
	err := extractVSIXBytes(t, data)
	if err == nil {
		t.Fatal("zip-bomb ratio not rejected")
	}
	if !strings.Contains(err.Error(), "compression ratio") && !strings.Contains(err.Error(), "limit") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestG20_VSIX_EntryCountLimitRejected(t *testing.T) {
	entries := map[string][]byte{}
	for i := 0; i < vsixMaxEntryCount+1; i++ {
		entries[fmt.Sprintf("extension/f%05d.txt", i)] = []byte("x")
	}
	data := buildVSIXZip(t, entries)
	err := extractVSIXBytes(t, data)
	if err == nil {
		t.Fatal("entry-count quota not enforced")
	}
	if !strings.Contains(err.Error(), "entry limit") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestG20_VSIX_PathLengthLimitRejected(t *testing.T) {
	longName := "extension/" + strings.Repeat("d", vsixMaxPathLength+50)
	data := buildVSIXZip(t, map[string][]byte{longName: []byte("x")})
	err := extractVSIXBytes(t, data)
	if err == nil {
		t.Fatal("path length quota not enforced")
	}
	if !strings.Contains(err.Error(), "path length") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestG20_VSIX_NestingDepthLimitRejected(t *testing.T) {
	depth := vsixMaxNestingDepth + 5
	name := "extension/" + strings.Repeat("a/", depth) + "leaf.txt"
	data := buildVSIXZip(t, map[string][]byte{name: []byte("x")})
	err := extractVSIXBytes(t, data)
	if err == nil {
		t.Fatal("nesting depth quota not enforced")
	}
	if !strings.Contains(err.Error(), "nesting depth") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// buildVSIXZipStored builds a zip with Store (no compression) so compression
// ratio checks do not preempt size-quota tests.
func buildVSIXZipStored(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		h := &zip.FileHeader{Name: name, Method: zip.Store}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestG20_VSIX_TotalExpandedSizeLimitRejected(t *testing.T) {
	// Several Store entries under the per-entry cap that together exceed the
	// total expanded budget (no compression, so ratio checks do not preempt).
	entries := map[string][]byte{}
	chunk := bytes.Repeat([]byte("B"), 45<<20) // 45 MiB each
	for i := 0; i < 5; i++ {                   // 225 MiB total
		entries[fmt.Sprintf("extension/f%d.bin", i)] = chunk
	}
	data := buildVSIXZipStored(t, entries)
	err := extractVSIXBytes(t, data)
	if err == nil {
		t.Fatal("total expanded size quota not enforced")
	}
	if !strings.Contains(err.Error(), "total expanded size") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestG20_VSIX_PerEntrySizeLimitRejected(t *testing.T) {
	// One entry exceeding the per-entry cap (header-declared size is checked
	// before any write; content is highly compressible so construction is fast).
	big := bytes.Repeat([]byte("C"), vsixMaxEntryBytes+(2<<20)) // 52 MiB
	data := buildVSIXZipStored(t, map[string][]byte{
		"extension/out/big.bin": big,
	})
	err := extractVSIXBytes(t, data)
	if err == nil {
		t.Fatal("per-entry size quota not enforced")
	}
	if !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Ensure no partial artifacts remain after a rejected extraction (atomic-ish:
// rejection happens before the target dir is fully populated; the caller
// removes the temp dir — here we just verify no file was written for the
// rejected entry).
func TestG20_VSIX_RejectedEntryLeavesNoFile(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// A valid small file first, then a bomb entry.
	w, _ := zw.Create("extension/ok.txt")
	_, _ = w.Write([]byte("ok"))
	big := bytes.Repeat([]byte("D"), 20<<20)
	w2, _ := zw.Create("extension/bomb.bin")
	_, _ = w2.Write(big)
	_ = zw.Close()

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if err := extractVSIXEntries(zr, dir); err == nil {
		t.Fatal("bomb not rejected")
	}
	if _, err := os.Stat(filepath.Join(dir, "extension", "bomb.bin")); !os.IsNotExist(err) {
		t.Fatalf("bomb entry file exists after rejection: %v", err)
	}
}

var _ io.Reader

func TestG20_VSIX_DuplicatePathRejected(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("extension/a.txt")
	_, _ = w.Write([]byte("first"))
	w2, _ := zw.Create("extension/./a.txt")
	_, _ = w2.Write([]byte("second"))
	_ = zw.Close()

	dir := t.TempDir()
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	err = extractVSIXEntries(zr, dir)
	if err == nil {
		t.Fatal("duplicate target path not rejected")
	}
	if !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestG20_VSIX_CaseCollisionRejected(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("extension/Readme.txt")
	_, _ = w.Write([]byte("r"))
	w2, _ := zw.Create("extension/readme.txt")
	_, _ = w2.Write([]byte("R"))
	_ = zw.Close()

	dir := t.TempDir()
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	err = extractVSIXEntries(zr, dir)
	if err == nil {
		t.Fatal("case-insensitive collision not rejected")
	}
	if !strings.Contains(err.Error(), "case-insensitive") {
		t.Fatalf("unexpected error: %v", err)
	}
}

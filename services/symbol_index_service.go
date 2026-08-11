package services

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"log/slog"
)

// symbol_index_service.go — G-COMP-01: Workspace symbol index for auto-import.
//
// This service scans the workspace for exported symbols in Go / TypeScript /
// JavaScript files and maintains an in-memory index. It enables auto-import
// (the "type b in a.js, press Enter, get import b from './b.js'" feature)
// even when no LSP server is running, and powers workspace symbol search
// (Ctrl+T-like functionality).
//
// Indexing strategy:
//   - On first query (lazy), scan the workspace root for .go/.ts/.tsx/.js/.jsx files
//   - Parse each file line-by-line for export declarations (no full AST — fast & dependency-free)
//   - Cache results with a content hash; re-parse only changed files
//   - Debounced re-index: the index refreshes at most once per 5 seconds
//
// Supported export forms:
//   Go:         func Foo(), type Bar struct, type Baz interface, var X, const Y
//   JS/TS ESM:  export default <expr>, export const/let/var X, export function F,
//               export class C, export type T, export interface I,
//               export { A, B as C } from './mod'
//   JS CJS:     module.exports = ..., module.exports.Foo = ..., exports.Foo = ...
//
// Thread-safety: all public methods are safe for concurrent use.

// SymbolKind labels the category of an indexed symbol (mirrors LSP SymbolKind
// values for direct use by the frontend, but kept as int to avoid coupling).
const (
	SymbolKindFile        = 1
	SymbolKindModule      = 2
	SymbolKindNamespace   = 3
	SymbolKindPackage     = 4
	SymbolKindClass       = 5
	SymbolKindMethod      = 6
	SymbolKindProperty    = 7
	SymbolKindField       = 8
	SymbolKindConstructor = 9
	SymbolKindEnum        = 10
	SymbolKindInterface   = 11
	SymbolKindFunction    = 12
	SymbolKindVariable    = 13
	SymbolKindConstant    = 14
)

// IndexedSymbol is a single exported symbol discovered in the workspace.
type IndexedSymbol struct {
	Name     string `json:"name"`
	Kind     int    `json:"kind"`
	FilePath string `json:"filePath"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	// ExportPath is the import path for this symbol:
	//   Go: "github.com/user/repo/pkg" (package path)
	//   JS/TS: "./path/to/b" (relative module path without extension)
	ExportPath string `json:"exportPath"`
	// IsDefaultExport is true for `export default` in JS/TS.
	IsDefaultExport bool `json:"isDefaultExport"`
	// Detail is a short description, e.g. "function Foo(x: number): string"
	Detail string `json:"detail,omitempty"`
}

// SymbolIndexService maintains an in-memory index of exported symbols across
// the workspace. It is the backend foundation for auto-import and workspace
// symbol search.
type SymbolIndexService struct {
	mu               sync.RWMutex
	workspaceRoot    string
	workspaceRoots   []string
	workspaceContext *WorkspaceContext
	// scanSourceFiles is injectable so scan/commit ordering can be tested deterministically.
	scanSourceFiles func(context.Context, string, indexBudget) ([]string, error)
	// parseSourceFile is injectable so parse/commit ordering can be tested deterministically.
	parseSourceFile func(string, string, []byte) []IndexedSymbol
	// symbols is the flat list of all indexed exported symbols.
	symbols []IndexedSymbol
	// fileMTimes records the last mtime of each indexed file for incremental updates.
	fileMTimes map[string]int64
	// fileHashes records content hashes so metadata-only changes do not trigger reparsing.
	fileHashes map[string]string
	// fileGenerations invalidates an in-flight IndexFile when the same path is removed.
	fileGenerations map[string]uint64
	// removalVersion invalidates an in-flight full rebuild after any removal event.
	removalVersion uint64
	// lastIndex tracks when the full index was last refreshed.
	lastIndex time.Time
	// indexVersion is bumped whenever a rebuilt index is published.
	indexVersion int
	// indexing coalesces concurrent lazy re-index passes.
	indexing bool
}

const (
	maxIndexFiles          = 100_000
	maxIndexFileBytes      = 10 * 1024 * 1024
	maxIndexAggregateBytes = 512 * 1024 * 1024
	maxIndexStableAttempts = 3
	maxSymbolSearchResults = 1_000
)

var ErrSymbolIndexBudgetExceeded = errors.New("symbol index budget exceeded")

type indexBudget struct {
	maxFiles          int
	maxFileBytes      int64
	maxAggregateBytes int64
	scannedFiles      int
	readFiles         int
	readBytes         int64
	processedFiles    int
	processedBytes    int64
}

type indexBudgetExceededError struct {
	limit         string
	reason        string
	path          string
	value         int64
	maximum       int64
	affectedFiles int
}

func (e *indexBudgetExceededError) Error() string {
	if e.path != "" {
		return fmt.Sprintf("symbol index %s limit exceeded by %q: %d > %d", e.limit, e.path, e.value, e.maximum)
	}
	return fmt.Sprintf("symbol index %s limit exceeded: %d > %d", e.limit, e.value, e.maximum)
}

func (e *indexBudgetExceededError) Unwrap() error {
	return ErrSymbolIndexBudgetExceeded
}

func defaultIndexBudget() indexBudget {
	return indexBudget{
		maxFiles:          maxIndexFiles,
		maxFileBytes:      maxIndexFileBytes,
		maxAggregateBytes: maxIndexAggregateBytes,
	}
}

// NewSymbolIndexService creates a new SymbolIndexService. The workspace root
// is empty until SetWorkspaceRoot is called.
func NewSymbolIndexService() *SymbolIndexService {
	return newSymbolIndexService(nil)
}

// NewSymbolIndexServiceWithWorkspaceContext creates the renderer-facing
// symbol index. Queries bind their scan and publish phases to one workspace
// generation instead of treating an empty root as an empty result.
func NewSymbolIndexServiceWithWorkspaceContext(workspaceContext *WorkspaceContext) *SymbolIndexService {
	return newSymbolIndexService(workspaceContext)
}

func newSymbolIndexService(workspaceContext *WorkspaceContext) *SymbolIndexService {
	return &SymbolIndexService{
		workspaceContext: workspaceContext,
		fileMTimes:       make(map[string]int64),
		fileHashes:       make(map[string]string),
		fileGenerations:  make(map[string]uint64),
		scanSourceFiles:  collectSourceFilesWithBudget,
		parseSourceFile:  parseFileExportsWithASTContentAtRoot,
	}
}

func (s *SymbolIndexService) workspaceLeaseAndRoots() (workspaceLease, []string, error) {
	s.mu.RLock()
	roots := s.workspaceRootsLocked()
	ctx := s.workspaceContext
	s.mu.RUnlock()
	if len(roots) == 0 {
		if ctx == nil {
			return workspaceLease{allowUnscoped: true}, nil, nil
		}
		return workspaceLease{}, nil, fmt.Errorf("symbol index workspace root is not set: %w", ErrNotAllowed)
	}
	lease, err := acquireWorkspaceLease(ctx, roots[0], 0)
	if err != nil {
		return workspaceLease{}, nil, err
	}
	if ctx != nil && !sameWorkspaceIdentityPath(roots[0], lease.root) {
		return workspaceLease{}, nil, fmt.Errorf("symbol index workspace switch is not committed: %w", ErrNotAllowed)
	}
	return lease, roots, nil
}

// setWorkspaceRoot updates the workspace root and invalidates the index.
// The next query will trigger a lazy re-scan.
//
//wails:ignore
func (s *SymbolIndexService) setWorkspaceRoot(root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workspaceRoot == root && len(s.workspaceRoots) == 0 {
		return
	}
	s.workspaceRoot = root
	s.workspaceRoots = nil
	s.resetIndexLocked()
}

// setWorkspaceRoots installs all roots used by lazy indexing and incremental
// file events. Validation completes before state is changed.
//
//wails:ignore
func (s *SymbolIndexService) setWorkspaceRoots(roots []string) error {
	if len(roots) == 0 {
		s.setWorkspaceRoot("")
		return nil
	}
	cleaned, err := canonicalizeExistingWorkspaceRoots(roots)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	targetMulti := cleaned
	if len(cleaned) == 1 {
		targetMulti = nil
	}
	if s.workspaceRoot == cleaned[0] && stringSlicesEqual(s.workspaceRoots, targetMulti) {
		return nil
	}
	s.workspaceRoot = cleaned[0]
	s.workspaceRoots = append([]string(nil), targetMulti...)
	s.resetIndexLocked()
	return nil
}

func (s *SymbolIndexService) WorkspaceRoots() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.workspaceRootsLocked()
}

func (s *SymbolIndexService) workspaceRootsLocked() []string {
	if len(s.workspaceRoots) > 0 {
		return append([]string(nil), s.workspaceRoots...)
	}
	if s.workspaceRoot == "" {
		return nil
	}
	return []string{s.workspaceRoot}
}

func (s *SymbolIndexService) rootsMatchLocked(roots []string) bool {
	return stringSlicesEqual(s.workspaceRootsLocked(), roots)
}

func (s *SymbolIndexService) resetIndexLocked() {
	s.symbols = nil
	s.fileMTimes = make(map[string]int64)
	s.fileHashes = make(map[string]string)
	s.fileGenerations = make(map[string]uint64)
	s.lastIndex = time.Time{}
	s.indexVersion++
}

// IndexFile incrementally replaces the symbols for one source file. File IO
// and parsing happen outside the service lock; a version check retries when a
// concurrent full rebuild or file update publishes first.
func (s *SymbolIndexService) IndexFile(filePath string) error {
	if !isSupportedSourceFile(filePath) {
		return fmt.Errorf("unsupported source file")
	}
	for {
		lease, roots, err := s.workspaceLeaseAndRoots()
		if err != nil {
			return err
		}
		s.mu.RLock()
		startedRemovalVersion := s.removalVersion
		s.mu.RUnlock()

		resolvedRoot, resolvedFile, err := resolveWorkspaceFileInRoots(roots, filePath)
		if err != nil {
			return err
		}

		s.mu.RLock()
		if !s.rootsMatchLocked(roots) {
			s.mu.RUnlock()
			continue
		}
		baseVersion := s.indexVersion
		baseGeneration := s.fileGenerations[resolvedFile]
		if baseGeneration > startedRemovalVersion {
			s.mu.RUnlock()
			return nil
		}
		currentHash, wasIndexed := s.fileHashes[resolvedFile]
		parseSourceFile := s.parseSourceFile
		s.mu.RUnlock()

		hash, mtime, err := sourceFileMetadata(resolvedFile)
		if err != nil {
			return err
		}

		if wasIndexed && currentHash == hash {
			s.mu.Lock()
			if s.rootsMatchLocked(roots) && s.fileGenerations[resolvedFile] != baseGeneration {
				s.mu.Unlock()
				return nil
			}
			if !s.rootsMatchLocked(roots) || s.indexVersion != baseVersion {
				s.mu.Unlock()
				continue
			}
			s.fileMTimes[resolvedFile] = mtime
			s.mu.Unlock()
			return nil
		}

		symbols := parseSourceFile(resolvedFile, resolvedRoot, nil)
		verifiedHash, verifiedMtime, err := sourceFileMetadata(resolvedFile)
		if err != nil {
			s.mu.RLock()
			removed := s.rootsMatchLocked(roots) && s.fileGenerations[resolvedFile] != baseGeneration
			s.mu.RUnlock()
			if removed {
				return nil
			}
			return err
		}
		if verifiedHash != hash {
			s.mu.RLock()
			removed := s.rootsMatchLocked(roots) && s.fileGenerations[resolvedFile] != baseGeneration
			s.mu.RUnlock()
			if removed {
				return nil
			}
			continue
		}

		s.mu.Lock()
		if err := lease.validateCurrent(); err != nil {
			s.mu.Unlock()
			return err
		}
		if s.rootsMatchLocked(roots) && s.fileGenerations[resolvedFile] != baseGeneration {
			s.mu.Unlock()
			return nil
		}
		if !s.rootsMatchLocked(roots) || s.indexVersion != baseVersion {
			s.mu.Unlock()
			continue
		}
		next := make([]IndexedSymbol, 0, len(s.symbols)+len(symbols))
		for _, symbol := range s.symbols {
			if filepath.Clean(symbol.FilePath) != resolvedFile {
				next = append(next, symbol)
			}
		}
		next = append(next, symbols...)
		s.symbols = next
		s.fileMTimes[resolvedFile] = verifiedMtime
		s.fileHashes[resolvedFile] = verifiedHash
		s.indexVersion++
		s.mu.Unlock()
		return nil
	}
}

// RemoveFile removes one file from the index without rebuilding the workspace.
// It is idempotent so duplicate file-system delete events are harmless.
func (s *SymbolIndexService) RemoveFile(filePath string) error {
	lease, roots, err := s.workspaceLeaseAndRoots()
	if err != nil {
		return err
	}
	_, resolvedFile, err := resolveWorkspaceFileInRoots(roots, filePath)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := lease.validateCurrent(); err != nil {
		return err
	}
	if !s.rootsMatchLocked(roots) {
		return fmt.Errorf("workspace root changed")
	}
	s.removalVersion++
	s.fileGenerations[resolvedFile] = s.removalVersion
	changed := false
	next := make([]IndexedSymbol, 0, len(s.symbols))
	for _, symbol := range s.symbols {
		if filepath.Clean(symbol.FilePath) == resolvedFile {
			changed = true
			continue
		}
		next = append(next, symbol)
	}
	if _, ok := s.fileMTimes[resolvedFile]; ok {
		changed = true
		delete(s.fileMTimes, resolvedFile)
	}
	if _, ok := s.fileHashes[resolvedFile]; ok {
		changed = true
		delete(s.fileHashes, resolvedFile)
	}
	if changed {
		s.symbols = next
		s.indexVersion++
	}
	return nil
}

// SearchSymbols returns indexed symbols whose name contains the query
// (case-insensitive substring match). Results are capped at limit; pass 0
// for the default cap of 100. If the index is empty or stale, it is refreshed
// first (lazy indexing).
func (s *SymbolIndexService) SearchSymbols(ctx context.Context, query string, limit int) ([]IndexedSymbol, error) {
	if limit <= 0 {
		limit = 100
	} else if limit > maxSymbolSearchResults {
		limit = maxSymbolSearchResults
	}
	if err := s.ensureIndexed(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	q := strings.ToLower(query)
	out := make([]IndexedSymbol, 0, limit)
	for _, sym := range s.symbols {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if q == "" || strings.Contains(strings.ToLower(sym.Name), q) {
			out = append(out, sym)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// GetAutoImportCandidates returns symbols matching the given name that could
// be auto-imported into the requesting file. Symbols from the same file are
// excluded (they are already in scope). This is the core query for the
// "type b, see hello, press Enter to import" feature.
func (s *SymbolIndexService) GetAutoImportCandidates(ctx context.Context, name, fromFilePath string) ([]IndexedSymbol, error) {
	if err := s.ensureIndexed(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]IndexedSymbol, 0, 20)
	// Use case-insensitive prefix matching so progressive typing (e.g. "hel"
	// matching "hello") returns candidates. Exact matches are sorted first.
	lower := strings.ToLower(name)
	for _, sym := range s.symbols {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		symLower := strings.ToLower(sym.Name)
		// Match by exact (case-insensitive) or prefix. This allows the user
		// to type partial names and still get auto-import suggestions.
		if symLower != lower && !strings.HasPrefix(symLower, lower) {
			continue
		}
		// Normalize for comparison (forward/back slashes).
		normSym := filepath.ToSlash(sym.FilePath)
		normFrom := filepath.ToSlash(fromFilePath)
		if normSym == normFrom {
			continue
		}
		out = append(out, sym)
		if len(out) >= 20 {
			break
		}
	}
	return out, nil
}

// GetIndexStats returns basic statistics about the index for diagnostics.
type IndexStats struct {
	SymbolCount   int    `json:"symbolCount"`
	FileCount     int    `json:"fileCount"`
	WorkspaceRoot string `json:"workspaceRoot"`
	LastIndex     string `json:"lastIndex"`
	IndexVersion  int    `json:"indexVersion"`
}

func (s *SymbolIndexService) GetIndexStats() IndexStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	last := ""
	if !s.lastIndex.IsZero() {
		last = s.lastIndex.Format(time.RFC3339)
	}
	return IndexStats{
		SymbolCount:   len(s.symbols),
		FileCount:     len(s.fileMTimes),
		WorkspaceRoot: s.workspaceRoot,
		LastIndex:     last,
		IndexVersion:  s.indexVersion,
	}
}

// --- internal indexing ---

// ensureIndexed triggers a lazy re-index if the index is empty or stale.
// Debounced: at most one re-index per 5 seconds. Concurrent calls are
// coalesced via the indexing flag.
func (s *SymbolIndexService) ensureIndexed(ctx context.Context) error {
	lease, roots, err := s.workspaceLeaseAndRoots()
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		return lease.validateCurrent()
	}
	s.mu.Lock()
	needIndex := len(s.symbols) == 0 || time.Since(s.lastIndex) > 5*time.Second
	if !needIndex || s.indexing {
		s.mu.Unlock()
		return lease.validateCurrent()
	}
	s.indexing = true
	s.mu.Unlock()

	err = s.reindex(ctx, roots[0])
	if err == nil {
		for _, root := range roots[1:] {
			if err = s.indexAdditionalWorkspaceRoot(ctx, roots, root); err != nil {
				break
			}
		}
	}
	s.mu.Lock()
	s.indexing = false
	if err == nil {
		err = lease.validateCurrent()
	}
	if err == nil && s.rootsMatchLocked(roots) {
		s.lastIndex = time.Now()
	} else if err == nil {
		err = fmt.Errorf("symbol index workspace changed before publish: %w", ErrNotAllowed)
	}
	s.mu.Unlock()
	return err
}

func (s *SymbolIndexService) indexAdditionalWorkspaceRoot(ctx context.Context, expectedRoots []string, root string) error {
	s.mu.RLock()
	if !s.rootsMatchLocked(expectedRoots) {
		s.mu.RUnlock()
		return nil
	}
	scanSourceFiles := s.scanSourceFiles
	s.mu.RUnlock()
	files, err := scanSourceFiles(ctx, root, defaultIndexBudget())
	if err != nil && !errors.Is(err, ErrSymbolIndexBudgetExceeded) {
		return fmt.Errorf("collect source files for %s: %w", root, err)
	}
	sort.Strings(files)
	for _, file := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.IndexFile(file); err != nil {
			return err
		}
	}
	return nil
}

// reindex scans the workspace for source files and parses their exports.
func (s *SymbolIndexService) reindex(ctx context.Context, root string) error {
	return s.reindexWithBudget(ctx, root, defaultIndexBudget())
}

func (s *SymbolIndexService) reindexWithBudget(ctx context.Context, root string, budget indexBudget) error {
	if budget.maxFiles < 0 || budget.maxFileBytes < 0 || budget.maxAggregateBytes < 0 {
		return fmt.Errorf("invalid symbol index budget")
	}

	s.mu.RLock()
	if root != s.workspaceRoot {
		s.mu.RUnlock()
		return nil
	}
	workspaceContext := s.workspaceContext
	baseVersion := s.indexVersion
	baseRemovalVersion := s.removalVersion
	oldSymbols := append([]IndexedSymbol(nil), s.symbols...)
	oldHashes := make(map[string]string, len(s.fileHashes))
	for file, hash := range s.fileHashes {
		oldHashes[file] = hash
	}
	scanSourceFiles := s.scanSourceFiles
	parseSourceFile := s.parseSourceFile
	s.mu.RUnlock()
	lease, err := acquireWorkspaceLease(workspaceContext, root, 0)
	if err != nil {
		return err
	}
	if workspaceContext != nil && !sameWorkspaceIdentityPath(root, lease.root) {
		return fmt.Errorf("symbol index workspace switch is not committed: %w", ErrNotAllowed)
	}

	files, scanErr := scanSourceFiles(ctx, root, budget)
	if scanErr != nil && !errors.Is(scanErr, ErrSymbolIndexBudgetExceeded) {
		return fmt.Errorf("collect source files: %w", scanErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sort.Strings(files)

	// Build a new index; swap atomically at the end. Budget exhaustion publishes
	// the files completed before the limit instead of discarding all progress.
	newSymbols := make([]IndexedSymbol, 0, len(files)*5)
	newMTimes := make(map[string]int64, len(files))
	newHashes := make(map[string]string, len(files))
	var stopErr *indexBudgetExceededError
	var oversizedErr *indexBudgetExceededError
	var unstableErr *indexBudgetExceededError

scanFiles:
	for _, file := range files {
		if budget.scannedFiles >= budget.maxFiles {
			stopErr = &indexBudgetExceededError{
				limit:   "maxIndexFiles",
				reason:  "source file scan count reached the configured limit",
				path:    file,
				value:   int64(budget.scannedFiles + 1),
				maximum: int64(budget.maxFiles),
			}
			break
		}
		budget.scannedFiles++

		resolvedRoot, resolvedFile, err := resolveWorkspaceFile(root, file)
		if err != nil {
			continue
		}

		for attempt := 1; attempt <= maxIndexStableAttempts; attempt++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := os.Stat(resolvedFile)
			if err != nil {
				break
			}
			if !info.Mode().IsRegular() {
				break
			}
			if info.Size() > budget.maxFileBytes {
				recordOversizedIndexFile(&oversizedErr, resolvedFile, info.Size(), budget.maxFileBytes)
				break
			}
			if budget.readBytes+info.Size() > budget.maxAggregateBytes {
				stopErr = &indexBudgetExceededError{
					limit:   "maxIndexAggregateBytes",
					reason:  "reading the next source snapshot would exceed the aggregate byte limit",
					path:    resolvedFile,
					value:   budget.readBytes + info.Size(),
					maximum: budget.maxAggregateBytes,
				}
				break scanFiles
			}

			snapshot, err := readStableSourceFile(
				resolvedFile,
				info,
				budget.maxFileBytes,
			)
			if snapshot.read {
				budget.readFiles++
				budget.readBytes += snapshot.bytesRead
				if budget.readBytes > budget.maxAggregateBytes {
					stopErr = &indexBudgetExceededError{
						limit:   "maxIndexAggregateBytes",
						reason:  "source snapshot reads exceeded the aggregate byte limit",
						path:    resolvedFile,
						value:   budget.readBytes,
						maximum: budget.maxAggregateBytes,
					}
					break scanFiles
				}
			}
			if err != nil {
				var limitErr *indexBudgetExceededError
				if errors.As(err, &limitErr) {
					recordOversizedIndexFile(&oversizedErr, resolvedFile, limitErr.value, limitErr.maximum)
					break
				}
				if errors.Is(err, errSourceFileChanged) {
					if attempt == maxIndexStableAttempts {
						recordUnstableIndexFile(&unstableErr, resolvedFile)
						break
					}
					continue
				}
				break
			}
			hash := hashSourceContent(snapshot.content)

			// Content hashes avoid reparsing files whose mtime changed without a
			// content change, while still detecting edits that preserve the mtime.
			oldHash, wasIndexed := oldHashes[resolvedFile]
			if wasIndexed && oldHash == hash {
				newMTimes[resolvedFile] = snapshot.mtime
				newHashes[resolvedFile] = hash
				for _, sym := range oldSymbols {
					if filepath.Clean(sym.FilePath) == resolvedFile {
						newSymbols = append(newSymbols, sym)
					}
				}
				budget.processedFiles++
				budget.processedBytes += int64(len(snapshot.content))
				break
			}

			syms := parseSourceFile(resolvedFile, resolvedRoot, snapshot.content)
			verified, err := readStableSourceFile(resolvedFile, snapshot.info, budget.maxFileBytes)
			if err != nil || hashSourceContent(verified.content) != hash {
				if attempt == maxIndexStableAttempts {
					recordUnstableIndexFile(&unstableErr, resolvedFile)
					break
				}
				continue
			}
			newMTimes[resolvedFile] = snapshot.mtime
			newHashes[resolvedFile] = hash
			newSymbols = append(newSymbols, syms...)
			budget.processedFiles++
			budget.processedBytes += int64(len(snapshot.content))
			break
		}
	}

	if stopErr == nil && scanErr != nil {
		if !errors.As(scanErr, &stopErr) {
			stopErr = &indexBudgetExceededError{
				limit:  "scan",
				reason: "source file discovery stopped because its budget was exceeded",
			}
		}
	}

	published := false
	if err := lease.withCurrent(func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.indexVersion != baseVersion || s.removalVersion != baseRemovalVersion || s.workspaceRoot != root {
			return nil
		}
		s.symbols = newSymbols
		s.fileMTimes = newMTimes
		s.fileHashes = newHashes
		s.indexVersion++
		published = true
		return nil
	}); err != nil {
		return err
	}
	if !published {
		return nil
	}
	if oversizedErr != nil {
		warnSymbolIndexBudgetExceeded(root, budget, oversizedErr)
	}
	if unstableErr != nil {
		warnSymbolIndexBudgetExceeded(root, budget, unstableErr)
	}
	if stopErr != nil {
		warnSymbolIndexBudgetExceeded(root, budget, stopErr)
	}
	slog.Debug(
		"symbol index rebuilt",
		"root", root,
		"scannedFiles", budget.scannedFiles,
		"readFiles", budget.readFiles,
		"readBytes", budget.readBytes,
		"processedFiles", budget.processedFiles,
		"processedBytes", budget.processedBytes,
		"symbols", len(newSymbols),
	)
	return nil
}

func warnSymbolIndexBudgetExceeded(root string, budget indexBudget, budgetErr *indexBudgetExceededError) {
	attrs := []any{
		"root", root,
		"processedFiles", budget.processedFiles,
		"processedBytes", budget.processedBytes,
		"scannedFiles", budget.scannedFiles,
		"readFiles", budget.readFiles,
		"readBytes", budget.readBytes,
		"limit", budgetErr.limit,
		"reason", budgetErr.reason,
		"value", budgetErr.value,
		"maximum", budgetErr.maximum,
		"maxIndexFiles", budget.maxFiles,
		"maxIndexFileBytes", budget.maxFileBytes,
		"maxIndexAggregateBytes", budget.maxAggregateBytes,
	}
	if budgetErr.path != "" {
		attrs = append(attrs, "path", budgetErr.path)
	}
	if budgetErr.affectedFiles > 0 {
		attrs = append(attrs, "affectedFiles", budgetErr.affectedFiles)
	}
	slog.Warn("symbol index budget exceeded; publishing partial results", attrs...)
}

func recordOversizedIndexFile(target **indexBudgetExceededError, path string, value, maximum int64) {
	if *target == nil {
		*target = &indexBudgetExceededError{
			limit:         "maxIndexFileBytes",
			reason:        "oversized source files were skipped",
			path:          path,
			value:         value,
			maximum:       maximum,
			affectedFiles: 1,
		}
		return
	}
	(*target).affectedFiles++
}

func recordUnstableIndexFile(target **indexBudgetExceededError, path string) {
	if *target == nil {
		*target = &indexBudgetExceededError{
			limit:         "maxIndexStableAttempts",
			reason:        "source files kept changing while being indexed and were skipped",
			path:          path,
			value:         maxIndexStableAttempts,
			maximum:       maxIndexStableAttempts,
			affectedFiles: 1,
		}
		return
	}
	(*target).affectedFiles++
}

var errSourceFileChanged = errors.New("source file changed while indexing")

type sourceFileSnapshot struct {
	content   []byte
	info      os.FileInfo
	mtime     int64
	bytesRead int64
	read      bool
}

func readStableSourceFile(filePath string, before os.FileInfo, maxFileBytes int64) (sourceFileSnapshot, error) {
	if before.Size() > maxFileBytes {
		return sourceFileSnapshot{}, &indexBudgetExceededError{
			limit:   "maxIndexFileBytes",
			reason:  "source file exceeds the per-file byte limit",
			path:    filePath,
			value:   before.Size(),
			maximum: maxFileBytes,
		}
	}
	file, err := os.Open(filePath)
	if err != nil {
		return sourceFileSnapshot{}, fmt.Errorf("open source file: %w", err)
	}
	snapshot := sourceFileSnapshot{read: true}
	readLimit := maxFileBytes + 1
	content, readErr := io.ReadAll(io.LimitReader(file, readLimit))
	closeErr := file.Close()
	snapshot.content = content
	snapshot.bytesRead = int64(len(content))
	if readErr != nil {
		return snapshot, fmt.Errorf("read source file: %w", readErr)
	}
	if closeErr != nil {
		return snapshot, fmt.Errorf("close source file: %w", closeErr)
	}

	after, err := os.Stat(filePath)
	if err != nil {
		return snapshot, fmt.Errorf("stat source file after read: %w", err)
	}
	if after.Size() > maxFileBytes {
		return snapshot, &indexBudgetExceededError{
			limit:   "maxIndexFileBytes",
			reason:  "source file grew beyond the per-file byte limit while being read",
			path:    filePath,
			value:   after.Size(),
			maximum: maxFileBytes,
		}
	}
	if int64(len(content)) != after.Size() || !sameSourceFileInfo(before, after) {
		return snapshot, errSourceFileChanged
	}
	snapshot.info = after
	snapshot.mtime = after.ModTime().UnixMilli()
	return snapshot, nil
}

func sameSourceFileInfo(left, right os.FileInfo) bool {
	return os.SameFile(left, right) && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

func hashSourceContent(content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("%x", sum)
}

func sourceFileMetadata(filePath string) (string, int64, error) {
	hash, mtime, _, err := sourceFileMetadataWithLimit(filePath, maxIndexFileBytes)
	return hash, mtime, err
}

func sourceFileMetadataWithLimit(filePath string, maxBytes int64) (string, int64, int64, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return "", 0, 0, fmt.Errorf("stat source file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", 0, 0, fmt.Errorf("source file is not regular")
	}
	if info.Size() > maxBytes {
		return "", 0, 0, &indexBudgetExceededError{
			limit:   "file_bytes",
			path:    filePath,
			value:   info.Size(),
			maximum: maxBytes,
		}
	}
	file, err := os.Open(filePath)
	if err != nil {
		return "", 0, 0, fmt.Errorf("open source file: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return "", 0, 0, fmt.Errorf("read source file: %w", err)
	}
	if int64(len(content)) > maxBytes {
		return "", 0, 0, &indexBudgetExceededError{
			limit:   "file_bytes",
			path:    filePath,
			value:   int64(len(content)),
			maximum: maxBytes,
		}
	}
	return hashSourceContent(content), info.ModTime().UnixMilli(), int64(len(content)), nil
}

// collectSourceFiles walks the workspace root collecting source files,
// skipping vendor/node_modules/.git directories. It returns paths collected
// before a budget limit so reindex can still publish a partial snapshot.
func collectSourceFiles(ctx context.Context, root string) ([]string, error) {
	return collectSourceFilesWithBudget(ctx, root, defaultIndexBudget())
}

func collectSourceFilesWithBudget(ctx context.Context, root string, budget indexBudget) ([]string, error) {
	var files []string
	var aggregateBytes int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			return nil // skip inaccessible
		}
		if info.IsDir() {
			name := info.Name()
			// Skip dependency / build directories.
			switch name {
			case "node_modules", "vendor", ".git", "dist", "build", ".next", ".nuxt", "target":
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".go", ".ts", ".tsx", ".js", ".jsx":
			if len(files) >= budget.maxFiles {
				return &indexBudgetExceededError{
					limit:   "maxIndexFiles",
					reason:  "source file discovery reached the configured file count limit",
					path:    path,
					value:   int64(len(files) + 1),
					maximum: int64(budget.maxFiles),
				}
			}
			if info.Size() <= budget.maxFileBytes && aggregateBytes+info.Size() > budget.maxAggregateBytes {
				return &indexBudgetExceededError{
					limit:   "maxIndexAggregateBytes",
					reason:  "source file discovery reached the configured aggregate byte limit",
					path:    path,
					value:   aggregateBytes + info.Size(),
					maximum: budget.maxAggregateBytes,
				}
			}
			if info.Size() <= budget.maxFileBytes {
				aggregateBytes += info.Size()
			}
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func isSupportedSourceFile(filePath string) bool {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".go", ".ts", ".tsx", ".js", ".jsx":
		return true
	default:
		return false
	}
}

func resolveWorkspaceFile(root, filePath string) (string, string, error) {
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace root: %w", err)
	}
	resolvedFile := filePath
	if !filepath.IsAbs(resolvedFile) {
		fromWorkingDir, absErr := filepath.Abs(resolvedFile)
		if absErr == nil {
			rel, relErr := filepath.Rel(resolvedRoot, fromWorkingDir)
			if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				resolvedFile = fromWorkingDir
			} else {
				resolvedFile = filepath.Join(resolvedRoot, resolvedFile)
			}
		} else {
			resolvedFile = filepath.Join(resolvedRoot, resolvedFile)
		}
	}
	resolvedFile, err = filepath.Abs(resolvedFile)
	if err != nil {
		return "", "", fmt.Errorf("resolve source file: %w", err)
	}
	resolvedRoot = filepath.Clean(resolvedRoot)
	resolvedFile = filepath.Clean(resolvedFile)
	rel, err := filepath.Rel(resolvedRoot, resolvedFile)
	if err != nil {
		return "", "", fmt.Errorf("resolve source file: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("source file is outside workspace root")
	}
	return resolvedRoot, resolvedFile, nil
}

func resolveWorkspaceFileInRoots(roots []string, filePath string) (string, string, error) {
	var lastErr error
	for _, root := range roots {
		resolvedRoot, resolvedFile, err := resolveWorkspaceFile(root, filePath)
		if err == nil {
			return resolvedRoot, resolvedFile, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("workspace root is not set")
	}
	return "", "", lastErr
}

// parseFileExports extracts exported symbols from a single source file.
// It uses line-by-line scanning (not a full AST) for speed and zero-dependency.
// This is intentionally a best-effort parser: it covers the common export
// forms but may miss complex edge cases. For full accuracy, the LSP server
// (gopls / typescript-language-server) is the authoritative source.
func parseFileExports(filePath, workspaceRoot string) []IndexedSymbol {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".go":
		return parseGoExports(filePath, workspaceRoot)
	case ".ts", ".tsx":
		return parseTSExports(filePath, workspaceRoot)
	case ".js", ".jsx":
		return parseJSExports(filePath, workspaceRoot)
	}
	return nil
}

// relExportPath converts an absolute file path to a relative import path.
//
//	Go:   the package import path (derived from directory relative to workspace root)
//	JS/TS: "./path/to/file" (relative, no extension)
func relExportPath(filePath, workspaceRoot string) string {
	rel, err := filepath.Rel(workspaceRoot, filePath)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	// Strip extension for JS/TS.
	ext := filepath.Ext(rel)
	if ext != "" {
		rel = strings.TrimSuffix(rel, ext)
	}
	// Ensure it starts with "./" for relative JS/TS imports.
	if !strings.HasPrefix(rel, "./") && !strings.HasPrefix(rel, "../") {
		rel = "./" + rel
	}
	return rel
}

// goPackagePath returns the Go package import path for a file, based on its
// directory relative to the workspace root. This is a best-effort derivation
// — the true module path requires reading go.mod.
func goPackagePath(filePath, workspaceRoot string) string {
	dir := filepath.Dir(filePath)
	rel, err := filepath.Rel(workspaceRoot, dir)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	// Try to read the module path from go.mod.
	modPath := findGoModPath(workspaceRoot)
	if modPath != "" && rel != "." {
		return modPath + "/" + rel
	}
	if modPath != "" {
		return modPath
	}
	return rel
}

// findGoModPath reads the go.mod in the workspace root and returns the module path.
func findGoModPath(workspaceRoot string) string {
	gomod := filepath.Join(workspaceRoot, "go.mod")
	f, err := os.Open(gomod)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// parseGoExports extracts exported symbols from a Go source file.
// Exported = starts with an uppercase letter.
func parseGoExports(filePath, workspaceRoot string) []IndexedSymbol {
	f, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseGoExportsReader(f, filePath, workspaceRoot)
}

func parseGoExportsContent(filePath, workspaceRoot string, content []byte) []IndexedSymbol {
	return parseGoExportsReader(bytes.NewReader(content), filePath, workspaceRoot)
}

func parseGoExportsReader(reader io.Reader, filePath, workspaceRoot string) []IndexedSymbol {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var syms []IndexedSymbol
	lineNum := 0
	pkgPath := goPackagePath(filePath, workspaceRoot)
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		// Detect: func, type, const, var declarations.
		// Exported names start with uppercase.
		sym := parseGoLine(line, pkgPath, filePath, lineNum)
		if sym != nil {
			syms = append(syms, sym...)
		}
	}
	return syms
}

// parseGoLine checks a single line for Go export declarations.
func parseGoLine(line, pkgPath, filePath string, lineNum int) []IndexedSymbol {
	// Remove leading "func (recv) " for methods — still exported.
	trimmed := line
	if strings.HasPrefix(trimmed, "func ") {
		// Method receiver: func (r *Type) MethodName(
		if strings.HasPrefix(trimmed, "func (") {
			// Find the method name after the closing paren.
			idx := strings.Index(trimmed, ") ")
			if idx < 0 {
				return nil
			}
			rest := strings.TrimSpace(trimmed[idx+2:])
			name := extractGoIdent(rest)
			if name != "" && isExportedGo(name) {
				return []IndexedSymbol{{
					Name: name, Kind: SymbolKindMethod,
					FilePath: filePath, Line: lineNum - 1, Column: 0,
					ExportPath: pkgPath,
					Detail:     line,
				}}
			}
			return nil
		}
		// Regular function: func FunctionName(
		rest := strings.TrimPrefix(trimmed, "func ")
		name := extractGoIdent(rest)
		if name != "" && isExportedGo(name) {
			return []IndexedSymbol{{
				Name: name, Kind: SymbolKindFunction,
				FilePath: filePath, Line: lineNum - 1, Column: 0,
				ExportPath: pkgPath,
				Detail:     line,
			}}
		}
		return nil
	}
	// type Type struct / type Type interface / type Type = alias
	if strings.HasPrefix(trimmed, "type ") {
		rest := strings.TrimPrefix(trimmed, "type ")
		name := extractGoIdent(rest)
		if name != "" && isExportedGo(name) {
			kind := SymbolKindClass
			if strings.Contains(rest, "interface") {
				kind = SymbolKindInterface
			} else if strings.Contains(rest, "type ") {
				kind = SymbolKindClass
			}
			return []IndexedSymbol{{
				Name: name, Kind: kind,
				FilePath: filePath, Line: lineNum - 1, Column: 0,
				ExportPath: pkgPath,
				Detail:     line,
			}}
		}
		return nil
	}
	// const / var blocks: const X = ... or var Y = ...
	// Also handles grouped: const ( X = 1; Y = 2 )
	if strings.HasPrefix(trimmed, "const ") || strings.HasPrefix(trimmed, "var ") {
		isConst := strings.HasPrefix(trimmed, "const ")
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "const "))
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "var "))
		// If it's a grouped declaration (starts with "("), skip — too complex for line scan.
		if strings.HasPrefix(rest, "(") {
			return nil
		}
		// Extract the name(s) before "=" or type.
		name := extractGoIdent(rest)
		if name != "" && isExportedGo(name) {
			kind := SymbolKindVariable
			if isConst {
				kind = SymbolKindConstant
			}
			return []IndexedSymbol{{
				Name: name, Kind: kind,
				FilePath: filePath, Line: lineNum - 1, Column: 0,
				ExportPath: pkgPath,
				Detail:     line,
			}}
		}
	}
	return nil
}

// extractGoIdent extracts the first identifier from a Go declaration line.
// E.g. "Foo(args)" → "Foo", "Bar struct {" → "Bar".
func extractGoIdent(s string) string {
	i := 0
	for i < len(s) && (isGoIdentChar(s[i])) {
		i++
	}
	if i == 0 {
		return ""
	}
	return s[:i]
}

func isGoIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

func isExportedGo(name string) bool {
	if name == "" {
		return false
	}
	c := name[0]
	return c >= 'A' && c <= 'Z'
}

// --- AST-based Go symbol parsing (Architecture improvement E) ---
//
// parseFileExportsWithAST dispatches symbol extraction. For .go files it
// prefers a high-fidelity AST parse (go/ast) and falls back to the
// regex-based parseGoExports on parse failure; other file types use the
// existing regex scanners unchanged. The regex path remains the fallback for
// non-Go files and for Go files with syntax errors.
func (s *SymbolIndexService) parseFileExportsWithAST(filePath string) []IndexedSymbol {
	s.mu.RLock()
	workspaceRoot := s.workspaceRoot
	s.mu.RUnlock()
	return parseFileExportsWithASTAtRoot(filePath, workspaceRoot)
}

func parseFileExportsWithASTAtRoot(filePath, workspaceRoot string) []IndexedSymbol {
	return parseFileExportsWithASTContentAtRoot(filePath, workspaceRoot, nil)
}

func parseFileExportsWithASTContentAtRoot(filePath, workspaceRoot string, content []byte) []IndexedSymbol {
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext == ".go" {
		syms, err := parseGoFileASTContentAtRoot(filePath, workspaceRoot, content)
		if err == nil {
			return syms
		}
		slog.Debug("go AST parse failed; falling back to regex", "file", filePath, "err", err)
		if content != nil {
			return parseGoExportsContent(filePath, workspaceRoot, content)
		}
		return parseGoExports(filePath, workspaceRoot)
	}
	if content != nil {
		return parseFileExportsContent(filePath, workspaceRoot, content)
	}
	return parseFileExports(filePath, workspaceRoot)
}

// parseGoFileAST parses a Go source file using the standard library go/ast
// package and extracts exported symbols with higher fidelity than the
// regex-based scanner: it correctly handles grouped const/var blocks, method
// receivers, type aliases, and multi-name declarations. Returns an error if
// the file cannot be parsed (caller should fall back to the regex scanner).
func (s *SymbolIndexService) parseGoFileAST(filePath string) ([]IndexedSymbol, error) {
	s.mu.RLock()
	workspaceRoot := s.workspaceRoot
	s.mu.RUnlock()
	return parseGoFileASTAtRoot(filePath, workspaceRoot)
}

func parseGoFileASTAtRoot(filePath, workspaceRoot string) ([]IndexedSymbol, error) {
	return parseGoFileASTContentAtRoot(filePath, workspaceRoot, nil)
}

func parseGoFileASTContentAtRoot(filePath, workspaceRoot string, content []byte) ([]IndexedSymbol, error) {
	fset := token.NewFileSet()
	var source any
	if content != nil {
		source = content
	}
	file, err := parser.ParseFile(fset, filePath, source, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse go file %q: %w", filePath, err)
	}
	pkgPath := goPackagePath(filePath, workspaceRoot)
	var syms []IndexedSymbol
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			syms = append(syms, funcDeclSymbols(fset, d, filePath, pkgPath)...)
		case *ast.GenDecl:
			syms = append(syms, genDeclSymbols(fset, d, filePath, pkgPath)...)
		}
	}
	return syms, nil
}

// funcDeclSymbols extracts a function or method symbol from an ast.FuncDecl.
// Only exported names (uppercase initial) are indexed.
func funcDeclSymbols(fset *token.FileSet, d *ast.FuncDecl, filePath, pkgPath string) []IndexedSymbol {
	if d.Name == nil || !isExportedGo(d.Name.Name) {
		return nil
	}
	kind := SymbolKindFunction
	recv := ""
	if d.Recv != nil && len(d.Recv.List) > 0 {
		kind = SymbolKindMethod
		recv = receiverTypeString(d.Recv.List[0].Type)
	}
	pos := fset.Position(d.Pos())
	return []IndexedSymbol{{
		Name:       d.Name.Name,
		Kind:       kind,
		FilePath:   filePath,
		Line:       pos.Line - 1,
		Column:     pos.Column - 1,
		ExportPath: pkgPath,
		Detail:     buildFuncDetail(fset, d, recv),
	}}
}

// genDeclSymbols extracts type / variable / constant symbols from an
// ast.GenDecl. Grouped declarations (const ( ... ) / var ( ... )) are handled
// — a case the regex scanner skips.
func genDeclSymbols(fset *token.FileSet, d *ast.GenDecl, filePath, pkgPath string) []IndexedSymbol {
	var syms []IndexedSymbol
	switch d.Tok {
	case token.TYPE:
		for _, spec := range d.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name == nil || !isExportedGo(ts.Name.Name) {
				continue
			}
			kind, detail := typeKindAndDetail(fset, ts)
			pos := fset.Position(ts.Pos())
			syms = append(syms, IndexedSymbol{
				Name:       ts.Name.Name,
				Kind:       kind,
				FilePath:   filePath,
				Line:       pos.Line - 1,
				Column:     pos.Column - 1,
				ExportPath: pkgPath,
				Detail:     detail,
			})
		}
	case token.VAR, token.CONST:
		kind := SymbolKindVariable
		if d.Tok == token.CONST {
			kind = SymbolKindConstant
		}
		for _, spec := range d.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range vs.Names {
				if name.Name == "" || !isExportedGo(name.Name) {
					continue
				}
				pos := fset.Position(name.Pos())
				syms = append(syms, IndexedSymbol{
					Name:       name.Name,
					Kind:       kind,
					FilePath:   filePath,
					Line:       pos.Line - 1,
					Column:     pos.Column - 1,
					ExportPath: pkgPath,
					Detail:     buildValueDetail(d.Tok, name.Name, vs, fset),
				})
			}
		}
	}
	return syms
}

// typeKindAndDetail maps an ast.TypeSpec to a symbol kind and a short detail
// string. Structs map to SymbolKindClass (mirroring the regex scanner and LSP
// Go conventions), interfaces to SymbolKindInterface, and aliases / defined
// types to SymbolKindClass.
func typeKindAndDetail(fset *token.FileSet, ts *ast.TypeSpec) (int, string) {
	switch t := ts.Type.(type) {
	case *ast.StructType:
		return SymbolKindClass, fmt.Sprintf("struct { %d fields }", structFieldCount(t.Fields))
	case *ast.InterfaceType:
		return SymbolKindInterface, fmt.Sprintf("interface { %d methods }", interfaceMethodCount(t.Methods))
	default:
		typ := renderType(fset, t)
		if ts.Assign != token.NoPos {
			return SymbolKindClass, "type " + ts.Name.Name + " = " + typ
		}
		return SymbolKindClass, "type " + ts.Name.Name + " " + typ
	}
}

// receiverTypeString renders the receiver type of a method, stripping the
// parameterization of generic receivers so the base type name is exposed
// (e.g. "*List[T]" -> "*List").
func receiverTypeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "*" + receiverTypeString(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return receiverTypeString(t.X)
	case *ast.IndexListExpr:
		return receiverTypeString(t.X)
	}
	return renderType(token.NewFileSet(), expr)
}

// structFieldCount returns the number of fields (including embedded ones) in a
// struct's field list. A declaration like `A, B int` counts as two fields.
func structFieldCount(fl *ast.FieldList) int {
	if fl == nil {
		return 0
	}
	n := 0
	for _, f := range fl.List {
		if len(f.Names) == 0 {
			n++ // embedded field
		} else {
			n += len(f.Names)
		}
	}
	return n
}

// interfaceMethodCount returns the number of methods (including embedded
// interfaces) in an interface's method set.
func interfaceMethodCount(fl *ast.FieldList) int {
	if fl == nil {
		return 0
	}
	n := 0
	for _, f := range fl.List {
		if len(f.Names) == 0 {
			n++ // embedded interface
		} else {
			n += len(f.Names)
		}
	}
	return n
}

// buildFuncDetail renders a function/method signature string for the Detail
// field, e.g. "func Hello(name string) string" or "func (*MyType) Greet()".
func buildFuncDetail(fset *token.FileSet, d *ast.FuncDecl, recv string) string {
	var b strings.Builder
	b.WriteString("func ")
	if recv != "" {
		b.WriteString("(")
		b.WriteString(recv)
		b.WriteString(") ")
	}
	b.WriteString(d.Name.Name)
	b.WriteString("(")
	b.WriteString(renderFieldList(fset, d.Type.Params))
	b.WriteString(")")
	if d.Type != nil && d.Type.Results != nil && len(d.Type.Results.List) > 0 {
		b.WriteString(" ")
		// Single unnamed result: render the type directly (no parens).
		if len(d.Type.Results.List) == 1 && len(d.Type.Results.List[0].Names) == 0 {
			b.WriteString(renderType(fset, d.Type.Results.List[0].Type))
		} else {
			b.WriteString("(")
			b.WriteString(renderFieldList(fset, d.Type.Results))
			b.WriteString(")")
		}
	}
	return b.String()
}

// buildValueDetail renders a short detail string for a var/const declaration.
func buildValueDetail(tok token.Token, name string, vs *ast.ValueSpec, fset *token.FileSet) string {
	prefix := "var "
	if tok == token.CONST {
		prefix = "const "
	}
	if vs.Type != nil {
		return prefix + name + " " + renderType(fset, vs.Type)
	}
	return prefix + name
}

// renderType renders an ast.Expr (a type expression) back to source text using
// go/printer.
func renderType(fset *token.FileSet, expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, fset, expr)
	return buf.String()
}

// renderFieldList renders a parameter/result field list as a comma-separated
// string, e.g. "name string, count int" or "error" (for unnamed fields).
func renderFieldList(fset *token.FileSet, fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	var parts []string
	for _, f := range fl.List {
		typ := renderType(fset, f.Type)
		if len(f.Names) == 0 {
			parts = append(parts, typ)
		} else {
			names := make([]string, 0, len(f.Names))
			for _, n := range f.Names {
				names = append(names, n.Name)
			}
			parts = append(parts, strings.Join(names, ", ")+" "+typ)
		}
	}
	return strings.Join(parts, ", ")
}

// parseTSExports extracts exported symbols from a TypeScript file.
func parseTSExports(filePath, workspaceRoot string) []IndexedSymbol {
	return parseESTSExports(filePath, workspaceRoot, true)
}

// parseJSExports extracts exported symbols from a JavaScript file.
// Supports both ESM (export) and CommonJS (module.exports) forms.
func parseJSExports(filePath, workspaceRoot string) []IndexedSymbol {
	syms := parseESTSExports(filePath, workspaceRoot, false)
	// Also scan for CommonJS exports.
	syms = append(syms, parseCJSExports(filePath, workspaceRoot)...)
	return syms
}

func parseFileExportsContent(filePath, workspaceRoot string, content []byte) []IndexedSymbol {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".go":
		return parseGoExportsContent(filePath, workspaceRoot, content)
	case ".ts", ".tsx":
		return parseESTSExportsContent(filePath, workspaceRoot, content, true)
	case ".js", ".jsx":
		syms := parseESTSExportsContent(filePath, workspaceRoot, content, false)
		return append(syms, parseCJSExportsContent(filePath, workspaceRoot, content)...)
	default:
		return nil
	}
}

// parseESTSExports handles ESM export forms shared by JS and TS.
func parseESTSExports(filePath, workspaceRoot string, isTS bool) []IndexedSymbol {
	f, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseESTSExportsReader(f, filePath, workspaceRoot, isTS)
}

func parseESTSExportsContent(filePath, workspaceRoot string, content []byte, isTS bool) []IndexedSymbol {
	return parseESTSExportsReader(bytes.NewReader(content), filePath, workspaceRoot, isTS)
}

func parseESTSExportsReader(reader io.Reader, filePath, workspaceRoot string, isTS bool) []IndexedSymbol {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	exportPath := relExportPath(filePath, workspaceRoot)
	var syms []IndexedSymbol
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		syms = append(syms, parseESExportLine(line, exportPath, filePath, lineNum, isTS)...)
	}
	return syms
}

// parseESExportLine checks a single line for ESM export forms.
func parseESExportLine(line, exportPath, filePath string, lineNum int, isTS bool) []IndexedSymbol {
	var syms []IndexedSymbol
	col := 0

	// export default ...
	if strings.HasPrefix(line, "export default ") {
		rest := strings.TrimSpace(strings.TrimPrefix(line, "export default "))
		// export default function Name(...
		if declaration, ok := stripJSDeclarationKeyword(rest, "function"); ok {
			declaration = strings.TrimSpace(strings.TrimPrefix(declaration, "*"))
			name := extractJSIdent(declaration)
			if name == "" {
				name = defaultExportIdentifier(filePath)
			}
			syms = append(syms, IndexedSymbol{
				Name: name, Kind: SymbolKindFunction,
				FilePath: filePath, Line: lineNum - 1, Column: col,
				ExportPath: exportPath, IsDefaultExport: true,
				Detail: line,
			})
			return syms
		}
		// export default class Name
		if declaration, ok := stripJSDeclarationKeyword(rest, "class"); ok {
			name := extractJSIdent(declaration)
			if name == "" {
				name = defaultExportIdentifier(filePath)
			}
			syms = append(syms, IndexedSymbol{
				Name: name, Kind: SymbolKindClass,
				FilePath: filePath, Line: lineNum - 1, Column: col,
				ExportPath: exportPath, IsDefaultExport: true,
				Detail: line,
			})
			return syms
		}
		// export default <expr> — the default export is the module's default.
		// Use a legal identifier derived from the file basename (like TypeScript).
		syms = append(syms, IndexedSymbol{
			Name: defaultExportIdentifier(filePath), Kind: SymbolKindVariable,
			FilePath: filePath, Line: lineNum - 1, Column: col,
			ExportPath: exportPath, IsDefaultExport: true,
			Detail: line,
		})
		return syms
	}

	// export const/let/var Name
	if strings.HasPrefix(line, "export ") {
		rest := strings.TrimPrefix(line, "export ")
		// export const Name = ...
		if strings.HasPrefix(rest, "const ") || strings.HasPrefix(rest, "let ") || strings.HasPrefix(rest, "var ") {
			declName := strings.TrimSpace(strings.SplitN(rest, " ", 2)[1])
			// Handle "Name = ..." or "Name: Type = ..."
			name := extractJSIdent(declName)
			if name != "" {
				syms = append(syms, IndexedSymbol{
					Name: name, Kind: SymbolKindVariable,
					FilePath: filePath, Line: lineNum - 1, Column: col,
					ExportPath: exportPath,
					Detail:     line,
				})
			}
			return syms
		}
		// export function Name(
		if strings.HasPrefix(rest, "function ") {
			name := extractJSIdent(strings.TrimPrefix(rest, "function "))
			if name != "" {
				syms = append(syms, IndexedSymbol{
					Name: name, Kind: SymbolKindFunction,
					FilePath: filePath, Line: lineNum - 1, Column: col,
					ExportPath: exportPath,
					Detail:     line,
				})
			}
			return syms
		}
		// export class Name
		if strings.HasPrefix(rest, "class ") {
			name := extractJSIdent(strings.TrimPrefix(rest, "class "))
			if name != "" {
				syms = append(syms, IndexedSymbol{
					Name: name, Kind: SymbolKindClass,
					FilePath: filePath, Line: lineNum - 1, Column: col,
					ExportPath: exportPath,
					Detail:     line,
				})
			}
			return syms
		}
		if isTS {
			// export interface Name
			if strings.HasPrefix(rest, "interface ") {
				name := extractJSIdent(strings.TrimPrefix(rest, "interface "))
				if name != "" {
					syms = append(syms, IndexedSymbol{
						Name: name, Kind: SymbolKindInterface,
						FilePath: filePath, Line: lineNum - 1, Column: col,
						ExportPath: exportPath,
						Detail:     line,
					})
				}
				return syms
			}
			// export type Name = ...
			if strings.HasPrefix(rest, "type ") {
				name := extractJSIdent(strings.TrimPrefix(rest, "type "))
				if name != "" {
					syms = append(syms, IndexedSymbol{
						Name: name, Kind: SymbolKindClass,
						FilePath: filePath, Line: lineNum - 1, Column: col,
						ExportPath: exportPath,
						Detail:     line,
					})
				}
				return syms
			}
			// export enum Name
			if strings.HasPrefix(rest, "enum ") {
				name := extractJSIdent(strings.TrimPrefix(rest, "enum "))
				if name != "" {
					syms = append(syms, IndexedSymbol{
						Name: name, Kind: SymbolKindEnum,
						FilePath: filePath, Line: lineNum - 1, Column: col,
						ExportPath: exportPath,
						Detail:     line,
					})
				}
				return syms
			}
		}
		// export { A, B as C } [from './mod']
		if strings.HasPrefix(rest, "{") {
			// Parse the named export list.
			names := parseNamedExportList(rest)
			for _, n := range names {
				syms = append(syms, IndexedSymbol{
					Name: n.Local, Kind: SymbolKindVariable,
					FilePath: filePath, Line: lineNum - 1, Column: col,
					ExportPath: exportPath,
					Detail:     line,
				})
			}
			return syms
		}
	}
	return nil
}

// parseCJSExports extracts CommonJS exports (module.exports / exports.X).
func parseCJSExports(filePath, workspaceRoot string) []IndexedSymbol {
	f, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseCJSExportsReader(f, filePath, workspaceRoot)
}

func parseCJSExportsContent(filePath, workspaceRoot string, content []byte) []IndexedSymbol {
	return parseCJSExportsReader(bytes.NewReader(content), filePath, workspaceRoot)
}

func parseCJSExportsReader(reader io.Reader, filePath, workspaceRoot string) []IndexedSymbol {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	exportPath := relExportPath(filePath, workspaceRoot)
	var syms []IndexedSymbol
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		// module.exports.Foo = ... or exports.Foo = ...
		if strings.HasPrefix(line, "module.exports.") {
			rest := strings.TrimPrefix(line, "module.exports.")
			name := extractJSIdent(rest)
			if name != "" && name != "exports" {
				syms = append(syms, IndexedSymbol{
					Name: name, Kind: SymbolKindVariable,
					FilePath: filePath, Line: lineNum - 1, Column: 0,
					ExportPath: exportPath,
					Detail:     line,
				})
			}
			continue
		}
		if strings.HasPrefix(line, "exports.") {
			rest := strings.TrimPrefix(line, "exports.")
			name := extractJSIdent(rest)
			if name != "" {
				syms = append(syms, IndexedSymbol{
					Name: name, Kind: SymbolKindVariable,
					FilePath: filePath, Line: lineNum - 1, Column: 0,
					ExportPath: exportPath,
					Detail:     line,
				})
			}
			continue
		}
		// module.exports = <default value>
		if strings.HasPrefix(line, "module.exports") {
			assignment := strings.TrimSpace(strings.TrimPrefix(line, "module.exports"))
			if !strings.HasPrefix(assignment, "=") {
				continue
			}
			rest := strings.TrimSpace(strings.TrimPrefix(assignment, "="))
			name := defaultExportIdentifier(filePath)
			kind := SymbolKindVariable
			if declaration, ok := stripJSDeclarationKeyword(rest, "function"); ok {
				declaration = strings.TrimSpace(strings.TrimPrefix(declaration, "*"))
				if explicitName := extractJSIdent(declaration); explicitName != "" {
					name = explicitName
				}
				kind = SymbolKindFunction
			} else if declaration, ok := stripJSDeclarationKeyword(rest, "class"); ok {
				if explicitName := extractJSIdent(declaration); explicitName != "" {
					name = explicitName
				}
				kind = SymbolKindClass
			}
			syms = append(syms, IndexedSymbol{
				Name: name, Kind: kind,
				FilePath: filePath, Line: lineNum - 1, Column: 0,
				ExportPath: exportPath, IsDefaultExport: true,
				Detail: line,
			})
		}
	}
	return syms
}

// parseNamedExportList parses "export { A, B as C }" and returns the names.
type exportName struct {
	Exported string
	Local    string
}

func parseNamedExportList(s string) []exportName {
	// Extract the content between { and }.
	start := strings.Index(s, "{")
	end := strings.Index(s, "}")
	if start < 0 || end < 0 || end <= start {
		return nil
	}
	inner := s[start+1 : end]
	parts := strings.Split(inner, ",")
	var names []exportName
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// "A" or "A as B"
		if asIdx := strings.Index(p, " as "); asIdx >= 0 {
			exported := strings.TrimSpace(p[:asIdx])
			local := strings.TrimSpace(p[asIdx+4:])
			names = append(names, exportName{Exported: exported, Local: local})
		} else {
			names = append(names, exportName{Exported: p, Local: p})
		}
	}
	return names
}

func stripJSDeclarationKeyword(value, keyword string) (string, bool) {
	if !strings.HasPrefix(value, keyword) {
		return "", false
	}
	remainder := value[len(keyword):]
	if remainder != "" {
		next, _ := utf8.DecodeRuneInString(remainder)
		if isJSIdentifierPart(next) {
			return "", false
		}
	}
	return strings.TrimSpace(remainder), true
}

// defaultExportIdentifier converts a module basename to a predictable local
// binding. Filename-derived names intentionally use ASCII only: non-ASCII and
// punctuation delimit words, an empty result falls back to defaultExport, and
// reserved words/numeric starts are prefixed with an underscore.
func defaultExportIdentifier(filePath string) string {
	baseName := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	if isValidASCIIJSIdentifier(baseName) {
		if isJSReservedWord(baseName) {
			return "_" + baseName
		}
		return baseName
	}

	var name strings.Builder
	capitalizeNext := false
	for _, candidate := range baseName {
		isASCIILetter := (candidate >= 'a' && candidate <= 'z') || (candidate >= 'A' && candidate <= 'Z')
		isASCIIDigit := candidate >= '0' && candidate <= '9'
		if !isASCIILetter && !isASCIIDigit {
			if name.Len() > 0 {
				capitalizeNext = true
			}
			continue
		}
		if capitalizeNext && candidate >= 'a' && candidate <= 'z' {
			candidate -= 'a' - 'A'
		}
		name.WriteRune(candidate)
		capitalizeNext = false
	}

	result := name.String()
	if result == "" {
		return "defaultExport"
	}
	if result[0] >= '0' && result[0] <= '9' {
		result = "_" + result
	}
	if isJSReservedWord(result) {
		result = "_" + result
	}
	return result
}

func isValidASCIIJSIdentifier(value string) bool {
	if value == "" || !isASCIIJSIdentifierStart(value[0]) {
		return false
	}
	for i := 1; i < len(value); i++ {
		if !isASCIIJSIdentifierPart(value[i]) {
			return false
		}
	}
	return true
}

func isASCIIJSIdentifierStart(candidate byte) bool {
	return (candidate >= 'a' && candidate <= 'z') ||
		(candidate >= 'A' && candidate <= 'Z') ||
		candidate == '_' || candidate == '$'
}

func isASCIIJSIdentifierPart(candidate byte) bool {
	return isASCIIJSIdentifierStart(candidate) || (candidate >= '0' && candidate <= '9')
}

func isJSIdentifierStart(candidate rune) bool {
	return candidate == '_' || candidate == '$' || unicode.IsLetter(candidate)
}

func isJSIdentifierPart(candidate rune) bool {
	return isJSIdentifierStart(candidate) ||
		unicode.IsDigit(candidate) ||
		unicode.Is(unicode.Mn, candidate) ||
		unicode.Is(unicode.Mc, candidate) ||
		unicode.Is(unicode.Pc, candidate) ||
		candidate == '\u200c' || candidate == '\u200d'
}

func isJSReservedWord(value string) bool {
	switch value {
	case "arguments", "await", "break", "case", "catch", "class", "const", "continue",
		"debugger", "default", "delete", "do", "else", "enum", "eval", "export", "extends",
		"false", "finally", "for", "function", "if", "implements", "import", "in", "instanceof",
		"interface", "let", "new", "null", "package", "private", "protected", "public", "return",
		"static", "super", "switch", "this", "throw", "true", "try", "typeof", "var", "void",
		"while", "with", "yield":
		return true
	default:
		return false
	}
}

// extractJSIdent extracts the first valid JS/TS identifier from a string.
func extractJSIdent(s string) string {
	end := 0
	for index, candidate := range s {
		if index == 0 {
			if !isJSIdentifierStart(candidate) {
				return ""
			}
		} else if !isJSIdentifierPart(candidate) {
			break
		}
		end = index + utf8.RuneLen(candidate)
	}
	return s[:end]
}

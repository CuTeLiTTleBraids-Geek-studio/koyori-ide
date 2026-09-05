package services

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Blame and commit graph queries with bounded caches and streaming parsers.
// BlameLine is the blame info for a single line in a file.
type BlameLine struct {
	Line    int    `json:"line"`
	Commit  string `json:"commit"`  // short SHA
	Author  string `json:"author"`  // author name
	Date    string `json:"date"`    // RFC3339 author timestamp
	Content string `json:"content"` // source line content
	Email   string `json:"email"`   // author email
	Time    string `json:"time"`    // deprecated alias of Date
	Summary string `json:"summary"` // commit message summary
}

type blameCacheKey struct {
	filePath  string
	startLine int
	endLine   int
}

type blameCacheEntry struct {
	contentHash  [sha256.Size]byte
	headRevision string
	lines        []BlameLine
}

type gitStreamRunner func(repoPath string, args ...string) (*bufio.Scanner, func() error, error)

// CommitGraphEntry is one locale-independent git log record for the commit graph.
type CommitGraphEntry struct {
	Hash    string   `json:"hash"`
	Parents []string `json:"parents"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
	Time    string   `json:"time"`
	Refs    []string `json:"refs"`
	Subject string   `json:"subject"`
}

const (
	maxBlameRange              = 5000
	maxBlameCacheEntries       = 256
	gitCommandTimeout          = 5 * time.Minute
	gitNetworkOperationTimeout = 5 * time.Minute
	gitMutationWaitTimeout     = 5 * time.Minute
	defaultCommitGraphLimit    = 50
	maxCommitGraphLimit        = 200
	commitGraphFieldSep        = "\x1f"
	commitGraphRecordSep       = "\x1e"
)

var (
	commitGraphBranchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	resolvedCommitRe    = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)
)

// GetBlameAtRevision returns blame information for a bounded line range at
// an optional revision. User-provided revisions are resolved after
// --end-of-options and only the resulting hexadecimal object ID is passed to
// blame, so a revision can never become a git option.
func (g *GitService) GetBlameAtRevision(repoPath, filePath string, startLine, endLine int, revision string) ([]BlameLine, error) {
	if err := g.validatePath(repoPath); err != nil {
		return nil, err
	}
	if err := g.validateFilePath(repoPath, filePath); err != nil {
		return nil, err
	}
	if !((startLine == 0 && endLine == 0) ||
		(startLine > 0 && endLine >= startLine && endLine-startLine+1 <= maxBlameRange)) {
		return nil, fmt.Errorf("invalid blame range %d:%d", startLine, endLine)
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return nil, err
	}
	defer release()

	resolvedRevision := ""
	if revision != "" {
		var err error
		resolvedRevision, err = g.resolveGitRevision(repoPath, revision)
		if err != nil {
			return nil, err
		}
	}
	available, err := g.blameTargetAvailable(repoPath, filePath, resolvedRevision)
	if err != nil {
		return nil, err
	}
	if !available {
		return []BlameLine{}, nil
	}

	args := []string{"blame", "--line-porcelain"}
	if startLine > 0 {
		args = append(args, fmt.Sprintf("-L%d,%d", startLine, endLine))
	}
	if resolvedRevision != "" {
		args = append(args, resolvedRevision)
	}
	args = append(args, "--", filePath)
	scanner, wait, err := g.runGitStream(repoPath, args...)
	if err != nil {
		return nil, err
	}
	result, parseErr, waitErr := finishGitBlameStream(scanner, wait)
	if parseErr != nil {
		return nil, errors.Join(parseErr, waitErr)
	}
	if waitErr != nil {
		return nil, waitErr
	}
	return result, nil
}

// GetBlameForRange returns blame information for the inclusive, 1-indexed
// line range. Results are cached by canonical file path, range, SHA-256
// content hash, and HEAD revision so repeated viewport requests avoid another
// blame process while edits and history changes invalidate the cache.
func (g *GitService) GetBlameForRange(repoPath, filePath string, startLine, endLine int) ([]BlameLine, error) {
	return g.getBlameForRange(repoPath, filePath, startLine, endLine, g.runGitStream)
}

func (g *GitService) getBlameForRange(
	repoPath, filePath string,
	startLine, endLine int,
	runStream gitStreamRunner,
) ([]BlameLine, error) {
	if repoPath == "" {
		return nil, errors.New("repository path cannot be empty")
	}
	if err := g.validatePath(repoPath); err != nil {
		return nil, err
	}
	if err := g.validateFilePath(repoPath, filePath); err != nil {
		return nil, err
	}
	if startLine <= 0 || endLine < startLine || endLine-startLine+1 > maxBlameRange {
		return nil, fmt.Errorf("invalid blame range %d:%d", startLine, endLine)
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return nil, err
	}
	defer release()

	absPath, err := filepath.Abs(filepath.Join(repoPath, filepath.Clean(filePath)))
	if err != nil {
		return nil, fmt.Errorf("resolve blame file: %w", err)
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read blame file: %w", err)
	}
	contentHash := sha256.Sum256(content)
	cacheKey := blameCacheKey{
		filePath:  filepath.Clean(absPath),
		startLine: startLine,
		endLine:   endLine,
	}
	headRevision, err := g.resolveGitRevision(repoPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve blame head: %w", err)
	}
	if lines, ok := g.cachedBlame(cacheKey, contentHash, headRevision); ok {
		return lines, nil
	}

	args := []string{
		"blame",
		"-L", fmt.Sprintf("%d,%d", startLine, endLine),
		"--line-porcelain",
		"--", filePath,
	}
	scanner, wait, err := runStream(repoPath, args...)
	if err != nil {
		return nil, err
	}
	lines, parseErr, waitErr := finishGitBlameStream(scanner, wait)
	if streamErr := errors.Join(parseErr, waitErr); streamErr != nil {
		return nil, streamErr
	}

	// Do not cache output if the file or repository history changed while git
	// blame was running.
	currentContent, readErr := os.ReadFile(absPath)
	currentHead, headErr := g.resolveGitRevision(repoPath, "HEAD")
	if readErr != nil {
		logGitDebugError("git: skip blame cache update after file read failure", readErr)
	}
	if headErr != nil {
		logGitDebugError("git: skip blame cache update after head resolution failure", headErr)
	}
	if readErr == nil && headErr == nil &&
		sha256.Sum256(currentContent) == contentHash && currentHead == headRevision {
		g.storeBlame(cacheKey, contentHash, headRevision, lines)
	}
	return cloneBlameLines(lines), nil
}

func (g *GitService) cachedBlame(
	key blameCacheKey,
	contentHash [sha256.Size]byte,
	headRevision string,
) ([]BlameLine, bool) {
	g.blameCacheMu.Lock()
	entry, ok := g.blameCache[key]
	hit := ok && entry.contentHash == contentHash && entry.headRevision == headRevision
	if hit {
		g.blameCacheOrder = promoteBlameCacheKey(g.blameCacheOrder, key)
	}
	g.blameCacheMu.Unlock()
	g.recordBlameCacheResult(hit)
	if !hit {
		return nil, false
	}
	return cloneBlameLines(entry.lines), true
}

func (g *GitService) recordBlameCacheResult(hit bool) {
	event := "miss"
	if hit {
		event = "hit"
		g.blameCacheHits.Add(1)
	} else {
		g.blameCacheMisses.Add(1)
	}
	hits := g.blameCacheHits.Load()
	misses := g.blameCacheMisses.Load()
	total := hits + misses
	hitRate := 0.0
	if total > 0 {
		hitRate = float64(hits) / float64(total)
	}
	slog.Debug("git blame cache", "event", event, "hits", hits, "misses", misses, "hit_rate", hitRate)
}

func (g *GitService) storeBlame(
	key blameCacheKey,
	contentHash [sha256.Size]byte,
	headRevision string,
	lines []BlameLine,
) {
	g.blameCacheMu.Lock()
	defer g.blameCacheMu.Unlock()
	if g.blameCache == nil {
		g.blameCache = make(map[blameCacheKey]blameCacheEntry)
	}
	if _, exists := g.blameCache[key]; !exists {
		if len(g.blameCacheOrder) >= maxBlameCacheEntries {
			oldest := g.blameCacheOrder[0]
			g.blameCacheOrder = g.blameCacheOrder[1:]
			delete(g.blameCache, oldest)
		}
		g.blameCacheOrder = append(g.blameCacheOrder, key)
	} else {
		g.blameCacheOrder = promoteBlameCacheKey(g.blameCacheOrder, key)
	}
	g.blameCache[key] = blameCacheEntry{
		contentHash:  contentHash,
		headRevision: headRevision,
		lines:        cloneBlameLines(lines),
	}
}

func promoteBlameCacheKey(order []blameCacheKey, key blameCacheKey) []blameCacheKey {
	for i := range order {
		if order[i] != key {
			continue
		}
		if i < len(order)-1 {
			copy(order[i:], order[i+1:])
			order[len(order)-1] = key
		}
		return order
	}
	return order
}

func cloneBlameLines(lines []BlameLine) []BlameLine {
	cloned := make([]BlameLine, len(lines))
	copy(cloned, lines)
	return cloned
}

// GetCommitGraph returns structured commit graph records.
func (g *GitService) GetCommitGraph(repoPath string, limit int, branch string, all bool) ([]CommitGraphEntry, error) {
	if err := g.validatePath(repoPath); err != nil {
		return nil, err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return nil, err
	}
	defer release()
	if _, err := g.openRepo(repoPath); err != nil {
		return nil, err
	}
	if branch != "" && !validCommitGraphBranch(branch) {
		return nil, fmt.Errorf("invalid branch name: %q", branch)
	}
	if limit <= 0 {
		limit = defaultCommitGraphLimit
	} else if limit > maxCommitGraphLimit {
		limit = maxCommitGraphLimit
	}

	const format = "%H%x1f%P%x1f%an%x1f%ae%x1f%aI%x1f%D%x1f%s%x1e"
	args := []string{
		"log",
		"--topo-order",
		"--date=iso-strict",
		fmt.Sprintf("--max-count=%d", limit),
		"--decorate=full",
		"--pretty=format:" + format,
	}
	if all {
		args = append(args, "--all")
	} else {
		revision := "HEAD"
		if branch != "" {
			revision = "refs/heads/" + branch
		}
		resolved, err := g.resolveGitRevision(repoPath, revision)
		if err != nil {
			if branch == "" {
				logGitDebugError("git: commit graph unavailable without a resolved head", err)
				return []CommitGraphEntry{}, nil
			}
			return nil, err
		}
		args = append(args, resolved)
	}
	args = append(args, "--")
	output, err := g.runGit(repoPath, args...)
	if err != nil {
		if all {
			logGitDebugError("git: all-reference commit graph unavailable", err)
			return []CommitGraphEntry{}, nil
		}
		return nil, err
	}
	return parseCommitGraph(output), nil
}

func validCommitGraphBranch(branch string) bool {
	return commitGraphBranchRe.MatchString(branch) &&
		!strings.Contains(branch, "..") &&
		!strings.Contains(branch, "//") &&
		!strings.Contains(branch, "@{") &&
		!strings.HasSuffix(branch, "/") &&
		!strings.HasSuffix(branch, ".")
}

func (g *GitService) resolveGitRevision(repoPath, revision string) (string, error) {
	if strings.TrimSpace(revision) != revision || revision == "" || strings.ContainsAny(revision, "\x00\r\n") {
		return "", fmt.Errorf("invalid git revision")
	}
	output, err := g.runGit(repoPath, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve git revision: %w", err)
	}
	resolved := strings.TrimSpace(output)
	if !resolvedCommitRe.MatchString(resolved) {
		return "", fmt.Errorf("git returned invalid revision")
	}
	return strings.ToLower(resolved), nil
}

func parseCommitGraph(output string) []CommitGraphEntry {
	records := strings.Split(output, commitGraphRecordSep)
	entries := make([]CommitGraphEntry, 0, len(records))
	for _, record := range records {
		fields := strings.SplitN(record, commitGraphFieldSep, 7)
		if len(fields) != 7 {
			continue
		}
		hash := strings.TrimSpace(fields[0])
		if !resolvedCommitRe.MatchString(hash) {
			continue
		}
		refs := make([]string, 0)
		for _, ref := range strings.Split(fields[5], ",") {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			ref = strings.ReplaceAll(ref, "refs/heads/", "")
			ref = strings.ReplaceAll(ref, "refs/remotes/", "")
			ref = strings.ReplaceAll(ref, "refs/tags/", "tag: ")
			refs = append(refs, ref)
		}
		entries = append(entries, CommitGraphEntry{
			Hash:    strings.ToLower(hash),
			Parents: strings.Fields(fields[1]),
			Author:  fields[2],
			Email:   fields[3],
			Time:    fields[4],
			Refs:    refs,
			Subject: fields[6],
		})
	}
	return entries
}

// GetBlame returns per-line blame information for a file using
// `git blame --line-porcelain`. This powers the inline blame decoration
// in the editor (author + commit message shown at the end of each line).
// Returns an empty slice for non-repo directories or untracked files.
//
// M-2: output is consumed line-by-line via bufio.Scanner over a stdout
// pipe instead of being buffered in full via CombinedOutput, so very
// large files no longer risk OOM. The internal commit metadata cache is
// bounded (recentCommitsLimit) to prevent unbounded growth.
func (g *GitService) GetBlame(repoPath, filePath string) ([]BlameLine, error) {
	return g.GetBlameRange(repoPath, filePath, 0, 0)
}

// GetBlameRange is like GetBlame but limits blame to lines [startLine, endLine]
// (1-indexed, inclusive) via `git blame -L start,end`. Either zero disables
// the range (blame the whole file). Limiting the range is the recommended
// way to bound blame output for very large files.
func (g *GitService) GetBlameRange(repoPath, filePath string, startLine, endLine int) ([]BlameLine, error) {
	if err := g.validatePath(repoPath); err != nil {
		return nil, err
	}
	if err := g.validateFilePath(repoPath, filePath); err != nil {
		return nil, err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return nil, err
	}
	defer release()
	available, err := g.blameTargetAvailable(repoPath, filePath, "")
	if err != nil {
		return nil, err
	}
	if !available {
		return []BlameLine{}, nil
	}
	args := []string{"blame", "--line-porcelain"}
	if startLine > 0 && endLine > 0 {
		args = append(args, fmt.Sprintf("-L%d,%d", startLine, endLine))
	} else if startLine > 0 {
		args = append(args, fmt.Sprintf("-L%d,", startLine))
	}
	args = append(args, "--", filePath)
	scanner, wait, err := g.runGitStream(repoPath, args...)
	if err != nil {
		return nil, err
	}
	result, parseErr, waitErr := finishGitBlameStream(scanner, wait)
	if parseErr != nil {
		return nil, errors.Join(parseErr, waitErr)
	}
	if waitErr != nil {
		return nil, waitErr
	}
	return result, nil
}

// blameTargetAvailable identifies only the documented benign blame states.
// All other repository, object, index, and process failures remain errors.
func (g *GitService) blameTargetAvailable(repoPath, filePath, revision string) (bool, error) {
	repo, err := g.openRepo(repoPath)
	if err != nil {
		if errors.Is(err, errNotARepo) {
			logGitDebugError("git: blame unavailable outside a repository", err)
			return false, nil
		}
		return false, err
	}

	var commit *object.Commit
	if revision != "" {
		commit, err = repo.CommitObject(plumbing.NewHash(revision))
	} else {
		var head *plumbing.Reference
		head, err = repo.Head()
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			logGitDebugError("git: blame unavailable without head", err)
			return false, nil
		}
		if err == nil {
			commit, err = repo.CommitObject(head.Hash())
		}
	}
	if err != nil {
		return false, err
	}

	gitPath := filepath.ToSlash(filepath.Clean(filePath))
	if _, err := commit.File(gitPath); err == nil {
		return true, nil
	} else if !errors.Is(err, object.ErrFileNotFound) {
		return false, err
	} else if revision != "" {
		logGitDebugError("git: blame target absent at revision", err)
		return false, nil
	}

	idx, err := repo.Storer.Index()
	if err != nil {
		return false, err
	}
	if _, err := idx.Entry(gitPath); err == nil {
		return true, nil
	} else if errors.Is(err, index.ErrEntryNotFound) {
		logGitDebugError("git: blame target is untracked", err)
		return false, nil
	} else {
		return false, err
	}
}

// recentCommitsLimit bounds the per-commit metadata cache used by
// parseGitBlameStream. With --line-porcelain, every line carries its own
// metadata block, so the cache is a minor optimisation for adjacent lines
// sharing a commit; bounding it prevents unbounded growth on very large
// files with many distinct commits.
const recentCommitsLimit = 256

// blameCommitInfo holds the cached metadata for a single commit SHA.
type blameCommitInfo struct {
	author, email, time, summary string
}

// parseGitBlame parses a `git blame --line-porcelain` output string into
// BlameLine[]. It is a thin wrapper around parseGitBlameStream kept for
// tests that pass literal strings. Production callers should use the
// streaming variant directly to avoid buffering the whole output.
func parseGitBlame(output string) []BlameLine {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	result := parseGitBlameStream(scanner)
	logGitDebugError("git: parse in-memory blame failed", scanner.Err())
	return result
}

// finishGitBlameStream always reaps the child after parsing. Reading Scanner.Err
// before wait lets callers distinguish parser failures from expected blame
// command failures while wait closes stdout before reaping the process.
func finishGitBlameStream(
	scanner *bufio.Scanner,
	wait func() error,
) ([]BlameLine, error, error) {
	result := parseGitBlameStream(scanner)
	var parseErr error
	if err := scanner.Err(); err != nil {
		parseErr = fmt.Errorf("parse git blame: %w", err)
	}
	return result, parseErr, wait()
}

// parseGitBlameStream parses `git blame --line-porcelain` output from a
// bufio.Scanner into BlameLine[] without buffering the entire output.
//
// Porcelain block layout (per blame line):
//  1. header:  "<40-sha> <orig-line> <final-line> [<num>]"
//  2. metadata: "author", "author-mail", "author-time", "summary", ...
//  3. content:  "\t<line content>"
//
// Because metadata comes AFTER the header, we defer emitting the BlameLine
// until the content line ("\t...") — at which point all metadata for that
// block has been parsed. This also fixes a latent bug in the previous
// implementation where the first occurrence of a commit had empty
// Author/Email/Time/Summary fields.
//
// The per-commit metadata cache is bounded to recentCommitsLimit entries
// (FIFO eviction) so a file with millions of distinct commits cannot grow
// the map unbounded.
func parseGitBlameStream(scanner *bufio.Scanner) []BlameLine {
	var result []BlameLine
	commitInfo := make(map[string]blameCommitInfo, recentCommitsLimit)
	commitOrder := make([]string, 0, recentCommitsLimit)

	var (
		pendingCommit  string
		pendingLine    int
		pendingAuthor  string
		pendingEmail   string
		pendingTime    string
		pendingSummary string
		seenHeader     bool
	)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		switch {
		case line[0] == '\t':
			// Content line — flush the pending BlameLine.
			if seenHeader {
				result = append(result, BlameLine{
					Line:    pendingLine,
					Commit:  shortSHA(pendingCommit),
					Author:  pendingAuthor,
					Date:    pendingTime,
					Content: line[1:],
					Email:   pendingEmail,
					Time:    pendingTime,
					Summary: pendingSummary,
				})
				seenHeader = false
			}
		case strings.HasPrefix(line, "author "):
			pendingAuthor = strings.TrimPrefix(line, "author ")
			updateBlameCache(commitInfo, &commitOrder, pendingCommit, func(i *blameCommitInfo) { i.author = pendingAuthor })
		case strings.HasPrefix(line, "author-mail "):
			mail := strings.TrimPrefix(line, "author-mail ")
			mail = strings.TrimPrefix(mail, "<")
			mail = strings.TrimSuffix(mail, ">")
			pendingEmail = mail
			updateBlameCache(commitInfo, &commitOrder, pendingCommit, func(i *blameCommitInfo) { i.email = mail })
		case strings.HasPrefix(line, "author-time "):
			tsStr := strings.TrimPrefix(line, "author-time ")
			// L-2: 直接使用标准库 strconv.Atoi,删除自定义 strconvAtoi 包装。
			if ts, err := strconv.Atoi(tsStr); err == nil {
				pendingTime = time.Unix(int64(ts), 0).UTC().Format(time.RFC3339)
				updateBlameCache(commitInfo, &commitOrder, pendingCommit, func(i *blameCommitInfo) { i.time = pendingTime })
			}
		case strings.HasPrefix(line, "summary "):
			pendingSummary = strings.TrimPrefix(line, "summary ")
			updateBlameCache(commitInfo, &commitOrder, pendingCommit, func(i *blameCommitInfo) { i.summary = pendingSummary })
		default:
			// Header line: <sha> <orig-line> <final-line> [<num>].
			// Validate the hash and line number so metadata such as a
			// multi-word "committer" value cannot be mistaken for a header.
			commit, finalLine, ok := parseGitBlameHeader(line)
			if !ok {
				continue
			}
			pendingCommit = commit
			pendingLine = finalLine
			if info, ok := commitInfo[pendingCommit]; ok {
				// Move to back of LRU order.
				pendingAuthor = info.author
				pendingEmail = info.email
				pendingTime = info.time
				pendingSummary = info.summary
				commitOrder = removeFromOrder(commitOrder, pendingCommit)
				commitOrder = append(commitOrder, pendingCommit)
			} else {
				pendingAuthor = ""
				pendingEmail = ""
				pendingTime = ""
				pendingSummary = ""
				// Bound cache size.
				if len(commitOrder) >= recentCommitsLimit {
					oldest := commitOrder[0]
					commitOrder = commitOrder[1:]
					delete(commitInfo, oldest)
				}
				commitOrder = append(commitOrder, pendingCommit)
				commitInfo[pendingCommit] = blameCommitInfo{}
			}
			seenHeader = true
		}
	}
	// Flush a trailing pending BlameLine if the output ended without a
	// content line (shouldn't happen with --line-porcelain but defensive).
	if seenHeader {
		result = append(result, BlameLine{
			Line:    pendingLine,
			Commit:  shortSHA(pendingCommit),
			Author:  pendingAuthor,
			Date:    pendingTime,
			Email:   pendingEmail,
			Time:    pendingTime,
			Summary: pendingSummary,
		})
	}
	return result
}

func parseGitBlameHeader(line string) (string, int, bool) {
	parts := strings.Fields(line)
	if len(parts) < 3 || !resolvedCommitRe.MatchString(parts[0]) {
		return "", 0, false
	}
	finalLine, err := strconv.Atoi(parts[2])
	if err != nil || finalLine <= 0 {
		return "", 0, false
	}
	return parts[0], finalLine, true
}

// updateBlameCache applies mutate to the cache entry for commit, evicting
// the oldest entry when the cache is full and the commit is new. It also
// moves the commit to the back of the LRU order slice.
func updateBlameCache(cache map[string]blameCommitInfo, order *[]string, commit string, mutate func(*blameCommitInfo)) {
	if commit == "" {
		return
	}
	info, ok := cache[commit]
	if !ok {
		if len(*order) >= recentCommitsLimit {
			oldest := (*order)[0]
			*order = (*order)[1:]
			delete(cache, oldest)
		}
		*order = append(*order, commit)
	}
	mutate(&info)
	cache[commit] = info
}

// removeFromOrder returns order with the first occurrence of v removed.
// It does not allocate when v is not present.
func removeFromOrder(order []string, v string) []string {
	for i, s := range order {
		if s == v {
			return append(order[:i], order[i+1:]...)
		}
	}
	return order
}

// shortSHA returns the first 8 characters of a SHA (or the whole string if
// shorter). Used for the Commit field of BlameLine.
func shortSHA(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}

// readBlobContent reads the full content of the blob identified by h from the
// repository's object store. Returns an empty string for a zero hash (missing
// side). Errors are returned so ListMergeConflicts never presents unreadable
// conflict content as an empty side.
func readBlobContent(repo *git.Repository, h plumbing.Hash) (string, error) {
	if h.IsZero() {
		return "", nil
	}
	blob, err := repo.BlobObject(h)
	if err != nil {
		return "", err
	}
	r, err := blob.Reader()
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := r.Close(); closeErr != nil {
			logGitDebugError("git: close conflict blob reader failed", closeErr)
		}
	}()
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

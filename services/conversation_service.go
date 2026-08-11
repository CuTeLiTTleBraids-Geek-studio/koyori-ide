package services

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ConversationMessage is a single message in a persisted conversation.
type ConversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Conversation is a saved AI chat conversation.
type Conversation struct {
	ID        string                `json:"id"`
	Title     string                `json:"title"`
	CreatedAt int64                 `json:"created_at"`
	Messages  []ConversationMessage `json:"messages"`
	// SystemPromptOverride (N-60): when non-empty, this conversation uses
	// a custom system prompt instead of the global appState.aiSystemPrompt.
	// This allows per-session persona customization (e.g. "strict code reviewer"
	// for one conversation, "creative brainstorm partner" for another).
	// Empty string means "use the global default".
	SystemPromptOverride string `json:"system_prompt_override,omitempty"`
	// Plan 11 Task 2 — conversation organization metadata.
	Tags      []string `json:"tags,omitempty"`
	Favorite  bool     `json:"favorite,omitempty"`
	Group     string   `json:"group,omitempty"`
	PersonaID string   `json:"persona_id,omitempty"`
	Mode      string   `json:"mode,omitempty"`
	// DeletedAt (Plan 11 Task 2): Unix timestamp when the conversation was
	// soft-deleted (moved to the recycle bin). 0 means active. Conversations
	// with DeletedAt > 0 are retained for 30 days before purge, allowing
	// restoration via the trash UI. The frontend filters DeletedAt == 0 for
	// the active list; the trash view shows DeletedAt > 0.
	DeletedAt int64 `json:"deleted_at,omitempty"`
	// SortOrder (Plan 11 Task 2 Step 6): manual ordering set by drag-and-drop
	// in the sidebar. 0 means "no manual order" — the conversation falls back
	// to CreatedAt-desc ordering. Non-zero values are compared ascending, so
	// the frontend assigns 1, 2, 3, ... when reordering. Kept on the struct
	// (rather than a separate index file) so a single Save persists the order.
	SortOrder int64 `json:"sort_order,omitempty"`
	// Revision is a monotonic counter bumped on every successful Save
	// (prompt-7 Task C / BUG-H6). Clients send ExpectedRevision for CAS.
	Revision int64 `json:"revision"`
	// UpdatedAt is Unix seconds of the last successful Save.
	UpdatedAt int64 `json:"updated_at"`
	// ExpectedRevision is write-intent only (not stored on disk). When non-nil
	// and the conversation file already exists, Save fails with
	// ErrConversationConflict unless disk.Revision matches *ExpectedRevision.
	// nil = no CAS check (create or legacy soft-update paths).
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
}

// ErrConversationConflict is returned when Save CAS fails (prompt-7 Task C).
var ErrConversationConflict = errors.New("conversation revision conflict: disk was modified by another window")

// ConversationFilter holds optional filter criteria for listing conversations.
// All fields are optional; zero values mean "no filter on this field".
type ConversationFilter struct {
	Query     string // case-insensitive substring match on title + message content
	Tag       string // exact tag match (empty = any)
	Favorite  *bool  // pointer so false is distinct from "no filter"
	Group     string // exact group match (empty = any)
	PersonaID string // exact persona match (empty = any)
	Mode      string // exact mode match (empty = any)
	// IncludeTrash: when true, soft-deleted conversations (DeletedAt > 0)
	// are included in the result. Default false excludes them.
	IncludeTrash bool
}

const (
	defaultConversationListLimit = 50
	maxConversationListLimit     = 200
)

// ConversationService persists AI conversations to disk as JSON files.
type ConversationService struct {
	storageDir string
}

// NewConversationService creates a ConversationService rooted at the given directory.
func NewConversationService(storageDir string) *ConversationService {
	return &ConversationService{storageDir: storageDir}
}

// defaultStorageDir returns the user config dir + "/koyori-ide/conversations".
func defaultStorageDir() string {
	home, err := os.UserConfigDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "koyori-ide", "conversations")
	}
	return filepath.Join(home, "koyori-ide", "conversations")
}

// ensureDir creates the storage directory if it doesn't exist.
func (s *ConversationService) ensureDir() error {
	dir := s.storageDir
	if dir == "" {
		dir = defaultStorageDir()
		s.storageDir = dir
	}
	return os.MkdirAll(dir, 0755)
}

// pathFor returns the absolute path for a conversation file after validating
// that id is a safe filename component. Returns an error if id is empty,
// contains path separators, parent traversal (".."), or is an absolute path.
//
// N-91: previously this used filepath.Join(s.storageDir, id+".json")
// without any sanitization, allowing id values like "../../etc/evil" to
// read/write/delete arbitrary .json files. We now use SafeNameJoin from
// pathsec.go which rejects path separators, parent traversal, and absolute
// paths via IsRelativePathSafe.
func (s *ConversationService) pathFor(id string) (string, error) {
	return SafeNameJoin(s.storageDir, id, ".json")
}

// Save writes a conversation to disk.
// prompt-7 Task C: bumps Revision/UpdatedAt; optional CAS via ExpectedRevision.
func (s *ConversationService) Save(conv Conversation) error {
	if conv.ID == "" {
		return errors.New("conversation ID is required")
	}
	// N-134: limit SystemPromptOverride to prevent excessive disk usage.
	const maxSystemPromptOverrideLen = 100_000
	if len(conv.SystemPromptOverride) > maxSystemPromptOverrideLen {
		return fmt.Errorf("system prompt override exceeds maximum length of %d characters", maxSystemPromptOverrideLen)
	}
	path, err := s.pathFor(conv.ID)
	if err != nil {
		return fmt.Errorf("invalid conversation ID: %w", err)
	}
	if err := s.ensureDir(); err != nil {
		return err
	}

	// CAS + revision bump (prompt-7 Task C / BUG-H6).
	if existing, lerr := s.Load(conv.ID); lerr == nil {
		if conv.ExpectedRevision != nil && *conv.ExpectedRevision != existing.Revision {
			return fmt.Errorf("%w (expected %d, disk %d)", ErrConversationConflict, *conv.ExpectedRevision, existing.Revision)
		}
		// Preserve CreatedAt if client sent zero.
		if conv.CreatedAt == 0 {
			conv.CreatedAt = existing.CreatedAt
		}
		conv.Revision = existing.Revision + 1
	} else {
		// New conversation (or unreadable file treated as create).
		if conv.Revision <= 0 {
			conv.Revision = 1
		}
		if conv.CreatedAt == 0 {
			conv.CreatedAt = time.Now().Unix()
		}
	}
	conv.UpdatedAt = time.Now().Unix()
	// Never persist the write-intent field.
	conv.ExpectedRevision = nil

	// G-SEC-09: atomic write (temp file + rename) so a crash mid-write
	// cannot corrupt a conversation file. 0600 because conversations are
	// user-private content.
	return atomicWriteJSON(path, conv, 0600)
}

// Load reads a conversation by ID.
func (s *ConversationService) Load(id string) (Conversation, error) {
	var conv Conversation
	path, err := s.pathFor(id)
	if err != nil {
		return conv, fmt.Errorf("invalid conversation ID: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return conv, err
	}
	if err := json.Unmarshal(data, &conv); err != nil {
		return conv, err
	}
	return conv, nil
}

// List returns the first bounded page of conversations, newest first.
func (s *ConversationService) List() ([]Conversation, error) {
	return s.listPage(nil, 0, defaultConversationListLimit)
}

// ListPage returns a bounded page of conversations, newest first.
func (s *ConversationService) ListPage(offset, limit int) ([]Conversation, error) {
	return s.listPage(nil, offset, limit)
}

func (s *ConversationService) listPage(filter *ConversationFilter, offset, limit int) ([]Conversation, error) {
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.storageDir)
	if err != nil {
		return nil, err
	}
	convs := make([]Conversation, 0)
	query := ""
	if filter != nil {
		query = strings.ToLower(filter.Query)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.storageDir, entry.Name()))
		if err != nil {
			continue
		}
		var conv Conversation
		if err := json.Unmarshal(data, &conv); err != nil {
			continue
		}
		if filter != nil && !conversationMatchesFilter(conv, *filter, query) {
			continue
		}
		convs = append(convs, conv)
	}
	sort.Slice(convs, func(i, j int) bool {
		if convs[i].CreatedAt != convs[j].CreatedAt {
			return convs[i].CreatedAt > convs[j].CreatedAt
		}
		return convs[i].ID < convs[j].ID
	})

	offset, limit = normalizeConversationPage(offset, limit)
	if offset >= len(convs) {
		return []Conversation{}, nil
	}
	end := offset + limit
	if end > len(convs) {
		end = len(convs)
	}
	return convs[offset:end], nil
}

func normalizeConversationPage(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultConversationListLimit
	} else if limit > maxConversationListLimit {
		limit = maxConversationListLimit
	}
	return offset, limit
}

// Delete soft-deletes a conversation by setting DeletedAt and re-saving.
// The file is retained on disk so it can be restored from the trash UI
// (Plan 11 Task 2). Use PurgeExpiredTrash to physically remove conversations
// soft-deleted more than 30 days ago.
func (s *ConversationService) Delete(id string) error {
	conv, err := s.Load(id)
	if err != nil {
		return fmt.Errorf("failed to load conversation for soft-delete: %w", err)
	}
	conv.DeletedAt = time.Now().Unix()
	return s.Save(conv)
}

// Restore un-deletes a soft-deleted conversation by clearing DeletedAt.
// No-op if the conversation is not soft-deleted. Returns an error if the
// conversation does not exist.
func (s *ConversationService) Restore(id string) error {
	conv, err := s.Load(id)
	if err != nil {
		return fmt.Errorf("failed to load conversation for restore: %w", err)
	}
	conv.DeletedAt = 0
	return s.Save(conv)
}

// PurgeExpiredTrash physically removes conversations whose DeletedAt is older
// than maxAge. Returns the number of purged conversations. Conversations that
// are not soft-deleted are never purged.
func (s *ConversationService) PurgeExpiredTrash(maxAge time.Duration) (int, error) {
	if err := s.ensureDir(); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(s.storageDir)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-maxAge).Unix()
	purged := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.storageDir, entry.Name()))
		if err != nil {
			continue
		}
		var conv Conversation
		if err := json.Unmarshal(data, &conv); err != nil {
			continue
		}
		if conv.DeletedAt > 0 && conv.DeletedAt < cutoff {
			path, err := s.pathFor(conv.ID)
			if err != nil {
				continue
			}
			if err := os.Remove(path); err == nil {
				purged++
			}
		}
	}
	return purged, nil
}

// ListWithFilter returns conversations matching the given filter, sorted by
// CreatedAt descending. When filter.IncludeTrash is false (default), soft-deleted
// conversations are excluded. Query performs a case-insensitive substring match
// on the title and all message contents.
func (s *ConversationService) ListWithFilter(filter ConversationFilter) ([]Conversation, error) {
	return s.listPage(&filter, 0, defaultConversationListLimit)
}

// ListWithFilterPage returns a bounded page after applying the filter.
func (s *ConversationService) ListWithFilterPage(filter ConversationFilter, offset, limit int) ([]Conversation, error) {
	return s.listPage(&filter, offset, limit)
}

func conversationMatchesFilter(c Conversation, filter ConversationFilter, query string) bool {
	if c.DeletedAt > 0 && !filter.IncludeTrash {
		return false
	}
	if filter.Favorite != nil && c.Favorite != *filter.Favorite {
		return false
	}
	if filter.Group != "" && c.Group != filter.Group {
		return false
	}
	if filter.PersonaID != "" && c.PersonaID != filter.PersonaID {
		return false
	}
	if filter.Mode != "" && c.Mode != filter.Mode {
		return false
	}
	if filter.Tag != "" && !containsString(c.Tags, filter.Tag) {
		return false
	}
	return query == "" || conversationMatchesQuery(c, query)
}

// UpdateTags sets the tags for a conversation.
func (s *ConversationService) UpdateTags(id string, tags []string) error {
	conv, err := s.Load(id)
	if err != nil {
		return fmt.Errorf("failed to load conversation: %w", err)
	}
	conv.Tags = tags
	return s.Save(conv)
}

// UpdateFavorite sets the favorite flag for a conversation.
func (s *ConversationService) UpdateFavorite(id string, favorite bool) error {
	conv, err := s.Load(id)
	if err != nil {
		return fmt.Errorf("failed to load conversation: %w", err)
	}
	conv.Favorite = favorite
	return s.Save(conv)
}

// UpdateGroup sets the group for a conversation.
func (s *ConversationService) UpdateGroup(id string, group string) error {
	conv, err := s.Load(id)
	if err != nil {
		return fmt.Errorf("failed to load conversation: %w", err)
	}
	conv.Group = group
	return s.Save(conv)
}

// UpdateSortOrder sets the manual drag-and-drop sort order for a conversation.
// Plan 11 Task 2 Step 6: the frontend reassigns 1..N for every visible
// conversation on each reorder, so this is a single-field update. A value of 0
// clears the manual order and falls back to CreatedAt-desc ordering.
func (s *ConversationService) UpdateSortOrder(id string, order int64) error {
	conv, err := s.Load(id)
	if err != nil {
		return fmt.Errorf("failed to load conversation: %w", err)
	}
	conv.SortOrder = order
	return s.Save(conv)
}

// containsString reports whether ss contains v.
func containsString(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

// conversationMatchesQuery reports whether the conversation title or any
// message content contains the (already lowercased) query string.
func conversationMatchesQuery(c Conversation, q string) bool {
	if strings.Contains(strings.ToLower(c.Title), q) {
		return true
	}
	for _, m := range c.Messages {
		if strings.Contains(strings.ToLower(m.Content), q) {
			return true
		}
	}
	return false
}

// UpdateTitle renames an existing conversation. Returns an error if the
// conversation doesn't exist or the new title is empty.
func (s *ConversationService) UpdateTitle(id string, title string) error {
	if strings.TrimSpace(title) == "" {
		return errors.New("title cannot be empty")
	}
	conv, err := s.Load(id)
	if err != nil {
		return fmt.Errorf("failed to load conversation: %w", err)
	}
	conv.Title = strings.TrimSpace(title)
	return s.Save(conv)
}

// GenerateConversationID returns a random 16-byte hex ID with a time-based prefix.
func (s *ConversationService) GenerateConversationID() string {
	return GenerateConversationID()
}

// GenerateTitle returns a title derived from the first user message.
// Truncates to 65 characters; returns "(new conversation)" for empty input.
func (s *ConversationService) GenerateTitle(firstMessage string) string {
	return GenerateTitle(firstMessage)
}

// GenerateConversationID returns a random 16-byte hex ID with a time-based prefix.
func GenerateConversationID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// GenerateTitle returns a title derived from the first user message.
// Truncates to 65 characters; returns "(new conversation)" for empty input.
func GenerateTitle(firstMessage string) string {
	s := strings.TrimSpace(firstMessage)
	if s == "" {
		return "(new conversation)"
	}
	const maxLen = 65
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

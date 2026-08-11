package services

// extension_blacklist.go — G-VSC-03 requirement 3: known-malicious
// extension blacklist.
//
// The blacklist is a set of "<publisher>.<name>" extension IDs that are
// blocked from installation and enablement. It is seeded with built-in
// defaults (defaultBlacklist) and can be extended at runtime via
// AddToBlacklist, which persists user additions to
// <configDir>/koyori-ide/extension-blacklist.json.
//
// The built-in entries cannot be removed at runtime — they represent
// IDs that have been observed in the wild as malicious (typosquatting
// on popular extensions, credential stealers, etc.). User-added entries
// can be removed via RemoveFromBlacklist.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// extensionBlacklistFileName is the on-disk file name for the
// user-extensible blacklist, written under <configDir>/koyori-ide/.
const extensionBlacklistFileName = "extension-blacklist.json"

// defaultBlacklist is the built-in set of known-malicious extension IDs.
// These are real-world examples of malicious VS Code extensions that
// have been identified (typosquats, credential stealers, etc.). The list
// is intentionally conservative — adding an ID here permanently blocks
// it, so entries should be well-attested.
//
// IDs are lowercase "<publisher>.<name>".
var defaultBlacklist = map[string]bool{
	// anabarban.anabarban: a known malicious extension that exfiltrated
	// environment variables and SSH keys.
	"anabarban.anabarban": true,
	// esbenp.prettier-vscode-stolen: a typosquat / stolen-repack of the
	// legitimate "esbenp.prettier-vscode" that shipped malicious code.
	"esbenp.prettier-vscode-stolen": true,
	// Additional attested malicious IDs (documented in VS Code marketplace
	// takedowns). Kept lowercase for case-insensitive matching.
	"marinhobrandao.node-exec-stolen": true,
	"markcoder.azure-pipeline-stolen": true,
}

// blacklistFile is the on-disk JSON shape for the user-extensible
// blacklist. Only user-added entries are persisted here; the built-in
// defaultBlacklist is merged at load time and never written to disk.
type blacklistFile struct {
	// Entries is the list of user-added "<publisher>.<name>" IDs.
	Entries []string `json:"entries"`
}

// loadBlacklistFile loads user-added entries from
// <configDir>/koyori-ide/extension-blacklist.json and merges them into the
// in-memory blacklist. Missing file or parse errors are silently
// ignored (best-effort) so a corrupt file cannot brick the service.
func (s *ExtensionSecurityService) loadBlacklistFile() {
	if s.configDir == "" {
		return
	}
	path := filepath.Join(s.configDir, "koyori-ide", extensionBlacklistFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var bf blacklistFile
	if err := json.Unmarshal(data, &bf); err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range bf.Entries {
		if id != "" {
			s.blacklist[id] = true
		}
	}
}

// saveBlacklistFile persists the user-added entries (i.e. entries that
// are NOT in defaultBlacklist) to disk. Acquires the mutex.
func (s *ExtensionSecurityService) saveBlacklistFile() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveBlacklistFileLocked()
}

// saveBlacklistFileLocked persists the user-added entries to disk. The
// caller MUST hold s.mu.
func (s *ExtensionSecurityService) saveBlacklistFileLocked() error {
	if s.configDir == "" {
		// No config dir — in-memory only. Not an error so callers can
		// use the service in test/embedded contexts without a config dir.
		return nil
	}
	// Only persist user-added entries; the built-in defaults are
	// re-merged at load time and should not clutter the user file.
	entries := make([]string, 0, len(s.blacklist))
	for id := range s.blacklist {
		if !defaultBlacklist[id] {
			entries = append(entries, id)
		}
	}
	sort.Strings(entries) // deterministic output
	bf := blacklistFile{Entries: entries}
	path := filepath.Join(s.configDir, "koyori-ide", extensionBlacklistFileName)
	// M-5: atomic write (temp+rename+0600) prevents half-written blacklist
	// state if the process crashes mid-write.
	return atomicWriteJSON(path, bf, 0600)
}

package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// WorkspaceHostEntry is the transport-neutral description of one immediate
// workspace child. URI never contains an operating-system path.
type WorkspaceHostEntry struct {
	Name     string
	URI      WorkspaceURI
	IsDir    bool
	Size     int64
	Modified time.Time
}

// WorkspaceHostStat is the minimal transport-neutral metadata returned by a
// workspace host.
type WorkspaceHostStat struct {
	URI      WorkspaceURI
	IsDir    bool
	Size     int64
	Modified time.Time
}

// LocalWorkspaceHost adapts the active WorkspaceContext to local filesystem
// operations. It is an internal Go API and is deliberately not a Wails service.
type LocalWorkspaceHost struct {
	workspaceContext  *WorkspaceContext
	hostID            string
	workspaceID       string
	hostInstanceNonce string
	rootMu            sync.Mutex
	rootHandle        localHostNoFollowRoot
	rootPath          string
	rootGeneration    uint64
}

// NewLocalWorkspaceHost creates an adapter for one local workspace identity.
// The context may be unopened; operations then fail closed until it has a root.
func NewLocalWorkspaceHost(workspaceContext *WorkspaceContext, workspaceID, hostInstanceNonce string) (*LocalWorkspaceHost, error) {
	if workspaceContext == nil {
		return nil, fmt.Errorf("workspace context is required: %w", ErrInvalidInput)
	}
	if _, err := NewLocalWorkspaceRef(workspaceID, 1, hostInstanceNonce); err != nil {
		return nil, err
	}
	host := &LocalWorkspaceHost{
		workspaceContext:  workspaceContext,
		hostID:            LocalHostID,
		workspaceID:       workspaceID,
		hostInstanceNonce: hostInstanceNonce,
	}
	workspaceContext.mu.RLock()
	defer workspaceContext.mu.RUnlock()
	if localHostNoFollowBindingSupported() && workspaceContext.root != "" && workspaceContext.generation != 0 {
		if err := host.bindNoFollowRootLocked(workspaceContext.root, workspaceContext.generation); err != nil {
			return nil, safeLocalHostError(err)
		}
	}
	return host, nil
}

func localHostNoFollowBindingSupported() bool {
	return localHostNoFollowPlatformSupported()
}

// Close releases the host's internal root handle. It is not a Wails service.
func (h *LocalWorkspaceHost) Close() error {
	if h == nil {
		return nil
	}
	h.rootMu.Lock()
	defer h.rootMu.Unlock()
	localHostNoFollowCloseRoot(&h.rootHandle)
	h.rootPath, h.rootGeneration = "", 0
	return nil
}

// RootURI returns the transport-neutral URI for the currently open workspace.
func (h *LocalWorkspaceHost) RootURI() (WorkspaceURI, error) {
	ref, err := h.WorkspaceRef()
	if err != nil {
		return WorkspaceURI{}, err
	}
	return ref.URI, nil
}

// WorkspaceRef returns a ref bound to the current context generation.
func (h *LocalWorkspaceHost) WorkspaceRef() (WorkspaceRef, error) {
	if h == nil || h.workspaceContext == nil {
		return WorkspaceRef{}, fmt.Errorf("local workspace host is not configured: %w", ErrNotAllowed)
	}
	root, generation := h.workspaceContext.Snapshot()
	if root == "" || generation == 0 {
		return WorkspaceRef{}, fmt.Errorf("no workspace is open: %w", ErrNotAllowed)
	}
	return NewLocalWorkspaceRef(h.workspaceID, generation, h.hostInstanceNonce)
}

// Resolve validates a resource and scope without exposing its local host path.
func (h *LocalWorkspaceHost) Resolve(uri WorkspaceURI, scope WorkspaceScope) (WorkspaceURI, error) {
	resolved, err := h.resolveHostPath(uri, scope)
	if err != nil {
		return WorkspaceURI{}, safeLocalHostError(err)
	}
	if err := resolved.lease.validateCurrent(); err != nil {
		return WorkspaceURI{}, safeLocalHostError(err)
	}
	return resolved.uri, nil
}

// ReadFile reads at most the same 20 MiB accepted by FileService.
func (h *LocalWorkspaceHost) ReadFile(uri WorkspaceURI, scope WorkspaceScope) ([]byte, error) {
	resolved, err := h.resolveHostPath(uri, scope)
	if err != nil {
		return nil, safeLocalHostError(err)
	}
	var data []byte
	err = resolved.lease.withCurrent(func() error {
		var err error
		root, release, err := h.boundNoFollowRootLocked(resolved.lease)
		if err != nil {
			return err
		}
		defer release()
		data, err = localHostNoFollowReadFile(root, resolved.uri.RelativePath(), maxReadableFileBytes)
		return err
	})
	return data, safeLocalHostError(err)
}

// WriteFile atomically replaces a file inside the active workspace.
func (h *LocalWorkspaceHost) WriteFile(uri WorkspaceURI, scope WorkspaceScope, data []byte) error {
	resolved, err := h.resolveHostPath(uri, scope)
	if err != nil {
		return safeLocalHostError(err)
	}
	err = resolved.lease.withCurrent(func() error {
		root, release, err := h.boundNoFollowRootLocked(resolved.lease)
		if err != nil {
			return err
		}
		defer release()
		if resolved.uri.RelativePath() == "" {
			return fmt.Errorf("workspace root cannot be overwritten: %w", ErrNotAllowed)
		}
		return localHostNoFollowWriteFile(root, resolved.uri.RelativePath(), data, 0o644)
	})
	return safeLocalHostError(err)
}

// ListDirectory returns immediate children using workspace URIs only.
func (h *LocalWorkspaceHost) ListDirectory(uri WorkspaceURI, scope WorkspaceScope) ([]WorkspaceHostEntry, error) {
	resolved, err := h.resolveHostPath(uri, scope)
	if err != nil {
		return nil, safeLocalHostError(err)
	}
	var result []WorkspaceHostEntry
	err = resolved.lease.withCurrent(func() error {
		root, release, err := h.boundNoFollowRootLocked(resolved.lease)
		if err != nil {
			return err
		}
		defer release()
		entries, err := localHostNoFollowReadDir(root, resolved.uri.RelativePath())
		if err != nil {
			return err
		}
		result = make([]WorkspaceHostEntry, 0, len(entries))
		for _, entry := range entries {
			childRelative := entry.Name
			if resolved.uri.RelativePath() != "" {
				childRelative = resolved.uri.RelativePath() + "/" + entry.Name
			}
			childURI, err := NewLocalWorkspaceURI(h.workspaceID, childRelative)
			if err != nil {
				return err
			}
			result = append(result, WorkspaceHostEntry{
				Name: entry.Name, URI: childURI, IsDir: entry.IsDir,
				Size: entry.Size, Modified: entry.Modified,
			})
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].IsDir != result[j].IsDir {
				return result[i].IsDir
			}
			return result[i].Name < result[j].Name
		})
		return nil
	})
	return result, safeLocalHostError(err)
}

// Stat returns metadata without leaking a local absolute path.
func (h *LocalWorkspaceHost) Stat(uri WorkspaceURI, scope WorkspaceScope) (WorkspaceHostStat, error) {
	resolved, err := h.resolveHostPath(uri, scope)
	if err != nil {
		return WorkspaceHostStat{}, safeLocalHostError(err)
	}
	var stat WorkspaceHostStat
	err = resolved.lease.withCurrent(func() error {
		root, release, err := h.boundNoFollowRootLocked(resolved.lease)
		if err != nil {
			return err
		}
		defer release()
		info, err := localHostNoFollowStat(root, resolved.uri.RelativePath())
		if err != nil {
			return err
		}
		stat = WorkspaceHostStat{
			URI: resolved.uri, IsDir: info.IsDir(), Size: info.Size(), Modified: info.ModTime(),
		}
		return nil
	})
	return stat, safeLocalHostError(err)
}

// boundNoFollowRootLocked is called only from workspaceLease.withCurrent, while
// the context read lock already proves lease.root and lease.generation current.
func (h *LocalWorkspaceHost) boundNoFollowRootLocked(lease workspaceLease) (localHostNoFollowRoot, func(), error) {
	h.rootMu.Lock()
	if h.rootPath != lease.root || h.rootGeneration != lease.generation {
		if err := h.bindNoFollowRootLocked(lease.root, lease.generation); err != nil {
			h.rootMu.Unlock()
			return localHostNoFollowRoot{}, nil, err
		}
	}
	return h.rootHandle, h.rootMu.Unlock, nil
}

func (h *LocalWorkspaceHost) bindNoFollowRootLocked(root string, generation uint64) error {
	localHostNoFollowCloseRoot(&h.rootHandle)
	h.rootPath, h.rootGeneration = "", 0
	h.rootHandle = localHostNoFollowBindRoot(root)
	if !localHostNoFollowRootValid(h.rootHandle) {
		return fmt.Errorf("workspace root handle unavailable: %w", ErrNotAllowed)
	}
	h.rootPath, h.rootGeneration = root, generation
	return nil
}

// localHostPath cannot cross the adapter boundary. Callers only receive its
// corresponding WorkspaceURI.
type localHostPath struct {
	path  string
	uri   WorkspaceURI
	lease workspaceLease
}

func (h *LocalWorkspaceHost) resolveHostPath(uri WorkspaceURI, scope WorkspaceScope) (localHostPath, error) {
	if h == nil || h.workspaceContext == nil {
		return localHostPath{}, fmt.Errorf("local workspace host is not configured: %w", ErrNotAllowed)
	}
	root, generation := h.workspaceContext.Snapshot()
	if root == "" || generation == 0 {
		return localHostPath{}, fmt.Errorf("no workspace is open: %w", ErrNotAllowed)
	}
	ref, err := NewLocalWorkspaceRef(h.workspaceID, generation, h.hostInstanceNonce)
	if err != nil {
		return localHostPath{}, err
	}
	if err := ref.MatchesScope(scope); err != nil {
		return localHostPath{}, err
	}
	canonical, err := NewWorkspaceURI(uri.HostID, uri.WorkspaceID, uri.RelativePath())
	if err != nil || canonical.String() != uri.String() {
		return localHostPath{}, fmt.Errorf("resource URI is not canonical: %w", ErrInvalidWorkspaceURI)
	}
	if h.hostID != LocalHostID || uri.HostID != h.hostID || uri.HostID != ref.HostID || uri.WorkspaceID != ref.WorkspaceID {
		return localHostPath{}, fmt.Errorf("resource does not belong to the active local workspace: %w", ErrUnsupportedWorkspace)
	}
	path := root
	if uri.RelativePath() != "" {
		path = filepath.Join(root, filepath.FromSlash(uri.RelativePath()))
	}
	// This is static symlink containment validation, not protection against a
	// symlink TOCTOU swap between validation and the filesystem operation.
	path, err = ValidatePathWithinRoot(root, path)
	if err != nil {
		return localHostPath{}, err
	}
	lease := workspaceLease{context: h.workspaceContext, root: root, generation: generation}
	if err := lease.validateCurrent(); err != nil {
		return localHostPath{}, err
	}
	return localHostPath{path: path, uri: canonical, lease: lease}, nil
}

// safeLocalHostError preserves stable classifications without exposing OS
// paths, temporary paths, or PathError text across the host boundary.
func safeLocalHostError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, ErrNotFound):
		return fmt.Errorf("workspace resource: %w", ErrNotFound)
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("workspace resource: %w", os.ErrPermission)
	case errors.Is(err, ErrInvalidWorkspaceURI):
		return fmt.Errorf("workspace request: %w", ErrInvalidWorkspaceURI)
	case errors.Is(err, ErrInvalidInput):
		return fmt.Errorf("workspace request: %w", ErrInvalidInput)
	case errors.Is(err, ErrStaleWorkspaceScope):
		return fmt.Errorf("workspace scope: %w", ErrStaleWorkspaceScope)
	case errors.Is(err, ErrUnsupportedWorkspace):
		return fmt.Errorf("workspace identity: %w", ErrUnsupportedWorkspace)
	case errors.Is(err, ErrDisconnectedWorkspace):
		return fmt.Errorf("workspace connection: %w", ErrDisconnectedWorkspace)
	case errors.Is(err, ErrNotAllowed):
		return fmt.Errorf("workspace operation: %w", ErrNotAllowed)
	default:
		return fmt.Errorf("workspace operation: %w", ErrNotAllowed)
	}
}

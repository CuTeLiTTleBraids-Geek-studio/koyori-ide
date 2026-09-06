package services

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const agentWorkflowDirectory = ".koyori-ide/workflows"

const (
	agentWorkflowLoadAfterReadDir = "after-read-dir"
	agentWorkflowLoadBeforeOpen   = "before-open"
	agentWorkflowLoadAfterOpen    = "after-open"
	agentWorkflowLoadAfterRead    = "after-read"
)

type agentWorkflowSource struct {
	Definition          WorkflowDef
	RelativePath        string
	ContentHash         string
	WorkspaceGeneration uint64
	FileGeneration      uint64
}

// loadAgentWorkflowSources is the execution-only workflow loader. It accepts
// no renderer-provided root and keeps enumeration plus reads under one
// FileService root capability and workspace lease.
func (s *WorkflowService) loadAgentWorkflowSources(fileService *FileService) ([]agentWorkflowSource, error) {
	if s == nil || fileService == nil {
		return nil, fmt.Errorf("workflow and file services are required: %w", ErrNotAllowed)
	}
	capability, err := fileService.acquireCapability(agentWorkflowDirectory, false)
	if err != nil {
		return nil, err
	}
	defer capability.releaseCapability()

	var sources []agentWorkflowSource
	err = capability.withCurrent(func() error {
		resolvedDirectory, resolveErr := capability.resolvedRelative(true)
		if resolveErr != nil {
			return fmt.Errorf("resolve Agent workflow directory: %w", resolveErr)
		}
		if cleanRootRelative(resolvedDirectory) != cleanRootRelative(capability.relative) {
			return fmt.Errorf("Agent workflow directory cannot be a link: %w", ErrNotAllowed)
		}
		directoryInfo, statErr := capability.root.root.Lstat(capability.relative)
		if os.IsNotExist(statErr) {
			sources = []agentWorkflowSource{}
			return nil
		}
		if statErr != nil {
			return fmt.Errorf("inspect Agent workflow directory: %w", statErr)
		}
		if directoryInfo.Mode()&fs.ModeSymlink != 0 || !directoryInfo.IsDir() {
			return fmt.Errorf("Agent workflow directory is not a regular directory: %w", ErrNotAllowed)
		}

		entries, readDirErr := fs.ReadDir(capability.root.root.FS(), capability.relative)
		if readDirErr != nil {
			return fmt.Errorf("read Agent workflow directory: %w", readDirErr)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		if hookErr := s.runAgentWorkflowLoadHook(agentWorkflowLoadAfterReadDir, agentWorkflowDirectory); hookErr != nil {
			return hookErr
		}

		workspaceGeneration := capability.lease.generation
		if workspaceGeneration == 0 {
			// Trusted headless callers may use a FileService without a shared
			// WorkspaceContext. Its root generation is still a capability epoch.
			workspaceGeneration = fileService.rootGeneration
		}
		if workspaceGeneration == 0 || capability.workspace.generation == 0 {
			return fmt.Errorf("Agent workflow workspace generation is unavailable: %w", ErrNotAllowed)
		}
		seenWorkflows := make(map[string]string)
		sources = make([]agentWorkflowSource, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			extension := strings.ToLower(filepath.Ext(name))
			if !hasWorkflowExt(extension) {
				continue
			}
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
				return fmt.Errorf("Agent workflow filename is invalid: %w", ErrNotAllowed)
			}
			relativePath := filepath.ToSlash(filepath.Join(agentWorkflowDirectory, name))
			data, contentHash, readErr := s.readAgentWorkflowSource(capability.root.root, relativePath)
			if readErr != nil {
				return fmt.Errorf("read Agent workflow %q: %w", relativePath, errors.Join(readErr, ErrNotAllowed))
			}
			definition, parseErr := parseWorkflow(data, extension)
			if parseErr != nil {
				return fmt.Errorf("parse Agent workflow %q: %w", relativePath, ErrInvalidInput)
			}
			if definition.Name == "" {
				definition.Name = strings.TrimSuffix(name, extension)
			}
			definition.Source = relativePath
			definition.RequiresConfirmation = true
			if !workflowIsValid(definition) {
				return fmt.Errorf("Agent workflow %q is invalid: %w", relativePath, ErrInvalidInput)
			}
			validation := s.ValidateWorkflow(definition)
			if !validation.Valid {
				return fmt.Errorf("Agent workflow %q is invalid: %v: %w", relativePath, validation.Errors, ErrInvalidInput)
			}
			if previous, duplicate := seenWorkflows[definition.Name]; duplicate {
				return fmt.Errorf("Agent workflow name %q is duplicated by %q and %q: %w", definition.Name, previous, relativePath, ErrNotAllowed)
			}
			seenWorkflows[definition.Name] = relativePath
			sources = append(sources, agentWorkflowSource{
				Definition:          *definition,
				RelativePath:        relativePath,
				ContentHash:         contentHash,
				WorkspaceGeneration: workspaceGeneration,
				FileGeneration:      capability.workspace.generation,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sources, nil
}

func (s *WorkflowService) readAgentWorkflowSource(root *os.Root, relativePath string) ([]byte, string, error) {
	if root == nil {
		return nil, "", fmt.Errorf("Agent workflow root is unavailable: %w", ErrNotAllowed)
	}
	before, err := root.Lstat(relativePath)
	if err != nil {
		return nil, "", err
	}
	if before.Mode()&fs.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, "", fmt.Errorf("Agent workflow source is not a regular file: %w", ErrNotAllowed)
	}
	if err := s.runAgentWorkflowLoadHook(agentWorkflowLoadBeforeOpen, relativePath); err != nil {
		return nil, "", err
	}
	handle, err := root.Open(relativePath)
	if err != nil {
		return nil, "", err
	}
	closed := false
	defer func() {
		if !closed {
			_ = handle.Close()
		}
	}()
	opened, err := handle.Stat()
	if err != nil {
		return nil, "", err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, "", fmt.Errorf("Agent workflow source changed before open: %w", ErrNotAllowed)
	}
	if opened.Size() > maxReadableFileBytes {
		return nil, "", fmt.Errorf("Agent workflow source exceeds the read limit: %w", ErrInvalidInput)
	}
	if err := s.runAgentWorkflowLoadHook(agentWorkflowLoadAfterOpen, relativePath); err != nil {
		return nil, "", err
	}
	data, readErr := io.ReadAll(io.LimitReader(handle, maxReadableFileBytes+1))
	if readErr != nil {
		return nil, "", readErr
	}
	if int64(len(data)) > maxReadableFileBytes {
		return nil, "", fmt.Errorf("Agent workflow source exceeds the read limit: %w", ErrInvalidInput)
	}
	if err := s.runAgentWorkflowLoadHook(agentWorkflowLoadAfterRead, relativePath); err != nil {
		return nil, "", err
	}
	after, err := root.Lstat(relativePath)
	if err != nil {
		return nil, "", err
	}
	if after.Mode()&fs.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(opened, after) {
		return nil, "", fmt.Errorf("Agent workflow source changed while reading: %w", ErrNotAllowed)
	}
	if err := handle.Close(); err != nil {
		return nil, "", err
	}
	closed = true
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func (s *WorkflowService) runAgentWorkflowLoadHook(stage, relativePath string) error {
	s.agentLoadMu.RLock()
	hook := s.agentLoadHook
	s.agentLoadMu.RUnlock()
	if hook == nil {
		return nil
	}
	return hook(stage, relativePath)
}

// setAgentWorkflowLoadHook installs a deterministic race hook for package
// tests. The callback must not call FileService methods because the loader
// holds the FileService capability lock while invoking it.
//
// 唯一调用方是 agent_execution_workflow_skill_windows_test.go（仅 Windows
// 编译）；GOOS=linux 的 lint 视角看不到它，故此处显式登记 unused 豁免，
// 避免 CI 的 Linux lint 腿误报死代码。
//
//wails:ignore
//nolint:unused // 仅被 GOOS=windows 的测试引用，Linux lint 视角不可见
func (s *WorkflowService) setAgentWorkflowLoadHook(hook func(stage, relativePath string) error) {
	s.agentLoadMu.Lock()
	s.agentLoadHook = hook
	s.agentLoadMu.Unlock()
}

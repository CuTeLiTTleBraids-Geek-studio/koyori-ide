package services

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

const externalReceiptRecoveryHandlePrefix = "receipt-recovery-v1:"

// externalReceiptRecoveryRecord is package-private so adapter-specific
// receipt IDs and UnitIDs cannot accidentally become a renderer DTO.
type externalReceiptRecoveryRecord struct {
	record UsageRecord
	handle string
}

// loadStableReceiptIdentityKey creates a per-config HMAC key only before the
// usage ledger exists. Once durable usage exists, losing or rotating this key
// must fail closed because it would invalidate outstanding operator handles.
func loadStableReceiptIdentityKey(path string, ledgerExists bool) (key []byte, retErr error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("receipt identity path is required: %w", ErrInvalidInput)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create receipt identity directory: %w", err)
	}
	lock, err := acquireAESKeyFileLock(path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("lock receipt identity key: %w", err)
	}
	defer func() {
		if releaseErr := lock.release(); releaseErr != nil && retErr == nil {
			key = nil
			retErr = fmt.Errorf("unlock receipt identity key: %w", releaseErr)
		}
	}()

	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		if ledgerExists {
			return nil, fmt.Errorf("open existing receipt identity key: %w", err)
		}
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("generate receipt identity key: %w", err)
		}
		if err := atomicWriteFile(path, []byte(hex.EncodeToString(key)), 0o600); err != nil {
			return nil, fmt.Errorf("persist receipt identity key: %w", err)
		}
		return key, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open existing receipt identity key: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat existing receipt identity key: %w", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("receipt identity key permissions %o are too broad", info.Mode().Perm())
	}
	data, err := io.ReadAll(io.LimitReader(file, 65))
	if err != nil {
		return nil, fmt.Errorf("read existing receipt identity key: %w", err)
	}
	if len(data) > 64 {
		return nil, fmt.Errorf("receipt identity key is oversized")
	}
	key, err = decodeAESKey(data)
	if err != nil {
		return nil, fmt.Errorf("decode existing receipt identity key: %w", err)
	}
	return key, nil
}

type externalReceiptHandleClaim struct {
	Version            int                     `json:"version"`
	UnitID             string                  `json:"unitId"`
	SessionID          string                  `json:"sessionId"`
	UnitKind           agentcore.UsageUnitKind `json:"unitKind"`
	Operation          string                  `json:"operation"`
	ExternalReceiptID  string                  `json:"externalReceiptId"`
	ExternalReversible bool                    `json:"externalReversible"`
	StartedAtUnixNano  int64                   `json:"startedAtUnixNano"`
}

func (s *AIPermissionService) externalReceiptHandle(rec UsageRecord) (string, error) {
	if s == nil || len(s.receiptIdentityKey) == 0 {
		if s != nil && s.receiptIdentityErr != nil {
			return "", s.receiptIdentityErr
		}
		return "", fmt.Errorf("receipt recovery identity is unavailable: %w", ErrNotAllowed)
	}
	claim := externalReceiptHandleClaim{
		Version: 1, UnitID: rec.UnitID, SessionID: rec.SessionID,
		UnitKind: agentcore.UsageUnitKind(rec.UnitKind), Operation: string(rec.Operation),
		ExternalReceiptID: rec.ExternalReceiptID, ExternalReversible: rec.ExternalReceiptReversible,
		StartedAtUnixNano: rec.StartedAt.UnixNano(),
	}
	encoded, err := json.Marshal(claim)
	if err != nil {
		return "", fmt.Errorf("encode receipt recovery handle: %w", err)
	}
	mac := hmac.New(sha256.New, s.receiptIdentityKey)
	_, _ = mac.Write([]byte("koyori-agent-external-receipt-v1\x00"))
	_, _ = mac.Write(encoded)
	return externalReceiptRecoveryHandlePrefix + hex.EncodeToString(mac.Sum(nil)), nil
}

func validExternalReceiptRecoveryHandle(handle string) bool {
	if len(handle) != len(externalReceiptRecoveryHandlePrefix)+sha256.Size*2 ||
		!strings.HasPrefix(handle, externalReceiptRecoveryHandlePrefix) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(handle, externalReceiptRecoveryHandlePrefix))
	return err == nil
}

func (s *AIPermissionService) pendingExternalReceiptRecovery() ([]externalReceiptRecoveryRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("permission service is unavailable: %w", ErrNotAllowed)
	}
	s.usageWriteMu.Lock()
	defer s.usageWriteMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usagePersistencePoison != nil {
		return nil, errors.Join(ErrUsagePersistencePoisoned, s.usagePersistencePoison)
	}
	if s.receiptIdentityErr != nil || len(s.receiptIdentityKey) == 0 {
		if s.receiptIdentityErr != nil {
			return nil, s.receiptIdentityErr
		}
		return nil, fmt.Errorf("receipt recovery identity is unavailable: %w", ErrNotAllowed)
	}
	rows := make([]externalReceiptRecoveryRecord, 0)
	for _, rec := range s.usage {
		if !rec.Pending || rec.ExternalReceiptID == "" {
			continue
		}
		handle, err := s.externalReceiptHandle(rec)
		if err != nil {
			return nil, err
		}
		rows = append(rows, externalReceiptRecoveryRecord{record: rec, handle: handle})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].record.UnitID < rows[j].record.UnitID })
	return rows, nil
}

func (s *AIPermissionService) externalReceiptRecoveryByHandleLocked(handle string, includeDisposed bool) (UsageRecord, string, error) {
	if !validExternalReceiptRecoveryHandle(handle) {
		return UsageRecord{}, "", fmt.Errorf("invalid external receipt recovery handle: %w", ErrInvalidInput)
	}
	for _, rec := range s.usage {
		if rec.ExternalReceiptID == "" || (!rec.Pending && !(includeDisposed && rec.ExternalCompensation == agentcore.ExternalCompensationManualUnknown)) {
			continue
		}
		candidate, err := s.externalReceiptHandle(rec)
		if err != nil {
			return UsageRecord{}, "", err
		}
		if hmac.Equal([]byte(candidate), []byte(handle)) {
			return rec, candidate, nil
		}
	}
	return UsageRecord{}, "", fmt.Errorf("external receipt recovery handle is not available: %w", ErrNotAllowed)
}

// applyExternalReceiptManualUnknown records an explicit terminal disposition
// without invoking an adapter. It is deliberately not a compensation path:
// after restart the adapter's private rollback state is unavailable, so the
// only honest generic outcome is an auditable unresolved/unknown terminal.
func (s *AIPermissionService) applyExternalReceiptManualUnknown(handle string) (UsageRecord, error) {
	if s == nil {
		return UsageRecord{}, fmt.Errorf("permission service is unavailable: %w", ErrNotAllowed)
	}
	s.usageWriteMu.Lock()
	defer s.usageWriteMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usagePersistencePoison != nil {
		return UsageRecord{}, errors.Join(ErrUsagePersistencePoisoned, s.usagePersistencePoison)
	}
	rec, _, err := s.externalReceiptRecoveryByHandleLocked(handle, true)
	if err != nil {
		return UsageRecord{}, err
	}
	if !rec.Pending {
		return rec, nil
	}
	terminal := rec
	terminal.Pending = false
	terminal.Success = false
	terminal.CompletedAt = time.Now()
	if terminal.CompletedAt.Before(terminal.StartedAt) {
		terminal.CompletedAt = terminal.StartedAt
	}
	terminal.Timestamp = terminal.CompletedAt
	terminal.ExternalCompensation = agentcore.ExternalCompensationManualUnknown
	terminal.Error = "manual disposition required"
	index, noop, transitionErr := s.usageTransitionLocked(terminal)
	if transitionErr != nil {
		return UsageRecord{}, transitionErr
	}
	if index < 0 || noop {
		return UsageRecord{}, fmt.Errorf("external receipt recovery row disappeared: %w", ErrUsageReceiptState)
	}
	commitState, appendErr := s.appendUsage(terminal)
	if appendErr != nil {
		if commitState == usagePersistencePublishedUnknown {
			s.usagePersistencePoison = errors.Join(ErrUsagePersistenceIndeterminate, appendErr)
			return UsageRecord{}, errors.Join(ErrUsagePersistenceIndeterminate, appendErr)
		}
		if errors.Is(appendErr, ErrUsagePersistencePoisoned) {
			s.usagePersistencePoison = errors.Join(ErrUsagePersistencePoisoned, appendErr)
			return UsageRecord{}, errors.Join(ErrUsagePersistencePoisoned, appendErr)
		}
		return UsageRecord{}, errors.Join(ErrAgentRecoveryPersistence, appendErr)
	}
	s.usage[index] = terminal
	return terminal, nil
}

// externalReceiptRecoveryForDisposition is a trusted package-internal lookup
// used for idempotent replay after the pending row has become terminal.
func (s *AIPermissionService) externalReceiptRecoveryForDisposition(handle string) (UsageRecord, string, error) {
	if s == nil {
		return UsageRecord{}, "", fmt.Errorf("permission service is unavailable: %w", ErrNotAllowed)
	}
	s.usageWriteMu.Lock()
	defer s.usageWriteMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usagePersistencePoison != nil {
		return UsageRecord{}, "", errors.Join(ErrUsagePersistencePoisoned, s.usagePersistencePoison)
	}
	if s.receiptIdentityErr != nil || len(s.receiptIdentityKey) == 0 {
		if s.receiptIdentityErr != nil {
			return UsageRecord{}, "", s.receiptIdentityErr
		}
		return UsageRecord{}, "", fmt.Errorf("receipt recovery identity is unavailable: %w", ErrNotAllowed)
	}
	return s.externalReceiptRecoveryByHandleLocked(handle, true)
}

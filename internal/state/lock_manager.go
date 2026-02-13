package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const (
	lockFileName  = "instance.flock"
	stateFileName = "state.json"
)

var (
	ErrBusy           = errors.New("claw is busy")
	ErrSourceConflict = errors.New("source path conflict")
	ErrInvalidState   = errors.New("invalid lock state")

	clawIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,127}$`)
)

type AcquireRequest struct {
	ClawID     string
	SourcePath string
	InstanceID string
	PID        int
}

type ReleaseRequest struct {
	ClawID string
}

type LockState struct {
	Active       bool      `json:"active"`
	InstanceID   string    `json:"instance_id,omitempty"`
	PID          int       `json:"pid,omitempty"`
	SourcePath   string    `json:"source_path,omitempty"`
	UpdatedAtUTC time.Time `json:"updated_at_utc"`
}

type LockHandle interface {
	Unlock() error
}

type Locker interface {
	TryLock(path string) (handle LockHandle, ok bool, err error)
}

type LockManager struct {
	root   string
	locker Locker
	now    func() time.Time
}

func NewLockManager(root string, locker Locker) *LockManager {
	if locker == nil {
		locker = NewFlockLocker()
	}
	return &LockManager{
		root:   root,
		locker: locker,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (m *LockManager) Acquire(ctx context.Context, req AcquireRequest) error {
	normalizedReq, err := normalizeAcquireRequest(req)
	if err != nil {
		return err
	}

	return m.withLock(normalizedReq.ClawID, func() error {
		return m.acquireLocked(ctx, normalizedReq)
	})
}

func (m *LockManager) AcquireWhileLocked(ctx context.Context, req AcquireRequest) error {
	normalizedReq, err := normalizeAcquireRequest(req)
	if err != nil {
		return err
	}
	if err := m.ensurePaths(normalizedReq.ClawID); err != nil {
		return err
	}
	return m.acquireLocked(ctx, normalizedReq)
}

func (m *LockManager) Release(ctx context.Context, req ReleaseRequest) error {
	normalizedReq, err := normalizeReleaseRequest(req)
	if err != nil {
		return err
	}

	return m.withLock(normalizedReq.ClawID, func() error {
		return m.releaseLocked(ctx, normalizedReq)
	})
}

func (m *LockManager) ReleaseWhileLocked(ctx context.Context, req ReleaseRequest) error {
	normalizedReq, err := normalizeReleaseRequest(req)
	if err != nil {
		return err
	}
	if err := m.ensurePaths(normalizedReq.ClawID); err != nil {
		return err
	}
	return m.releaseLocked(ctx, normalizedReq)
}

func (m *LockManager) WithInstanceLock(clawID string, fn func() error) error {
	if err := validateClawID(clawID); err != nil {
		return err
	}
	if fn == nil {
		return nil
	}
	return m.withLock(clawID, fn)
}

func (m *LockManager) Inspect(clawID string) (LockState, error) {
	if err := validateClawID(clawID); err != nil {
		return LockState{}, err
	}
	return readState(m.statePath(clawID))
}

func (m *LockManager) acquireLocked(ctx context.Context, req AcquireRequest) error {
	statePath := m.statePath(req.ClawID)

	state, err := readState(statePath)
	if err != nil {
		return err
	}

	if req.SourcePath != "" {
		if state.SourcePath != "" && state.SourcePath != req.SourcePath {
			return ErrSourceConflict
		}
		state.SourcePath = req.SourcePath
	}

	state.Active = true
	state.InstanceID = req.InstanceID
	state.PID = req.PID
	state.UpdatedAtUTC = m.now()
	return writeState(statePath, state)
}

func (m *LockManager) releaseLocked(ctx context.Context, req ReleaseRequest) error {
	statePath := m.statePath(req.ClawID)

	state, err := readState(statePath)
	if err != nil {
		return err
	}

	state.Active = false
	state.PID = 0
	state.InstanceID = ""
	state.UpdatedAtUTC = m.now()
	return writeState(statePath, state)
}

func normalizeAcquireRequest(req AcquireRequest) (AcquireRequest, error) {
	if err := validateClawID(req.ClawID); err != nil {
		return AcquireRequest{}, err
	}
	if req.SourcePath != "" {
		absSourcePath, err := filepath.Abs(req.SourcePath)
		if err != nil {
			return AcquireRequest{}, err
		}
		req.SourcePath = absSourcePath
	}
	return req, nil
}

func normalizeReleaseRequest(req ReleaseRequest) (ReleaseRequest, error) {
	if err := validateClawID(req.ClawID); err != nil {
		return ReleaseRequest{}, err
	}
	return req, nil
}

func (m *LockManager) ensurePaths(clawID string) error {
	clawDir := m.clawDir(clawID)
	if err := os.MkdirAll(clawDir, 0o755); err != nil {
		return err
	}
	return nil
}

func (m *LockManager) withLock(clawID string, fn func() error) error {
	if err := m.ensurePaths(clawID); err != nil {
		return err
	}

	handle, ok, err := m.locker.TryLock(m.lockPath(clawID))
	if err != nil {
		return err
	}
	if !ok {
		return ErrBusy
	}

	if err := fn(); err != nil {
		_ = handle.Unlock()
		return err
	}
	if err := handle.Unlock(); err != nil {
		return err
	}
	return nil
}

func (m *LockManager) clawDir(clawID string) string {
	return filepath.Join(m.root, clawID)
}

func (m *LockManager) lockPath(clawID string) string {
	return filepath.Join(m.clawDir(clawID), lockFileName)
}

func (m *LockManager) statePath(clawID string) string {
	return filepath.Join(m.clawDir(clawID), stateFileName)
}

func validateClawID(clawID string) error {
	if !clawIDPattern.MatchString(clawID) {
		return fmt.Errorf("invalid claw id %q", clawID)
	}
	return nil
}

func readState(path string) (LockState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LockState{}, nil
		}
		return LockState{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state LockState
	if err := decoder.Decode(&state); err != nil {
		return LockState{}, fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	return state, nil
}

func writeState(path string, state LockState) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	state.UpdatedAtUTC = state.UpdatedAtUTC.UTC()
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(state)
}

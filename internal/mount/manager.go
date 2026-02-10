package mount

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
	mountDirName  = "mount"
)

var (
	ErrBusy          = errors.New("claw is busy")
	ErrMountConflict = errors.New("mount source conflict")
	ErrInvalidState  = errors.New("invalid mount state")

	clawIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,127}$`)
)

type AcquireRequest struct {
	ClawID     string
	SourcePath string
	InstanceID string
	PID        int
}

type ReleaseRequest struct {
	ClawID  string
	Unmount bool
}

type State struct {
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

type Mounter interface {
	MountReadOnly(ctx context.Context, source string, target string) error
	Unmount(ctx context.Context, target string) error
	IsMounted(ctx context.Context, target string) (bool, error)
}

type Manager struct {
	root    string
	locker  Locker
	mounter Mounter
	now     func() time.Time
}

func NewManager(root string, locker Locker, mounter Mounter) *Manager {
	if locker == nil {
		locker = NewFlockLocker()
	}
	if mounter == nil {
		mounter = noopMounter{}
	}
	return &Manager{
		root:    root,
		locker:  locker,
		mounter: mounter,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (m *Manager) Acquire(ctx context.Context, req AcquireRequest) error {
	if err := validateClawID(req.ClawID); err != nil {
		return err
	}
	if req.SourcePath != "" {
		absSourcePath, err := filepath.Abs(req.SourcePath)
		if err != nil {
			return err
		}
		req.SourcePath = absSourcePath
	}

	return m.withLock(req.ClawID, func() error {
		statePath := m.statePath(req.ClawID)
		mountPath := m.mountPath(req.ClawID)

		state, err := readState(statePath)
		if err != nil {
			return err
		}

		if req.SourcePath != "" {
			mounted, err := m.mounter.IsMounted(ctx, mountPath)
			if err != nil {
				return err
			}
			if mounted && state.SourcePath != "" && state.SourcePath != req.SourcePath {
				return ErrMountConflict
			}
			if err := m.mounter.MountReadOnly(ctx, req.SourcePath, mountPath); err != nil {
				return err
			}
			state.SourcePath = req.SourcePath
		}

		state.Active = true
		state.InstanceID = req.InstanceID
		state.PID = req.PID
		state.UpdatedAtUTC = m.now()
		return writeState(statePath, state)
	})
}

func (m *Manager) Release(ctx context.Context, req ReleaseRequest) error {
	if err := validateClawID(req.ClawID); err != nil {
		return err
	}

	return m.withLock(req.ClawID, func() error {
		statePath := m.statePath(req.ClawID)
		mountPath := m.mountPath(req.ClawID)

		state, err := readState(statePath)
		if err != nil {
			return err
		}
		if req.Unmount {
			if err := m.mounter.Unmount(ctx, mountPath); err != nil {
				return err
			}
		}

		state.Active = false
		state.PID = 0
		state.InstanceID = ""
		state.UpdatedAtUTC = m.now()
		return writeState(statePath, state)
	})
}

func (m *Manager) Recover(ctx context.Context, clawID string) error {
	if err := validateClawID(clawID); err != nil {
		return err
	}

	return m.withLock(clawID, func() error {
		statePath := m.statePath(clawID)
		mountPath := m.mountPath(clawID)

		state, err := readState(statePath)
		if err != nil {
			if errors.Is(err, ErrInvalidState) {
				state = State{}
			} else {
				return err
			}
		}

		mounted, err := m.mounter.IsMounted(ctx, mountPath)
		if err != nil {
			return err
		}
		if mounted {
			if err := m.mounter.Unmount(ctx, mountPath); err != nil {
				return err
			}
		}

		state.Active = false
		state.PID = 0
		state.InstanceID = ""
		state.UpdatedAtUTC = m.now()
		return writeState(statePath, state)
	})
}

func (m *Manager) Inspect(clawID string) (State, error) {
	if err := validateClawID(clawID); err != nil {
		return State{}, err
	}
	return readState(m.statePath(clawID))
}

func (m *Manager) withLock(clawID string, fn func() error) error {
	clawDir := m.clawDir(clawID)
	if err := os.MkdirAll(clawDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(m.mountPath(clawID), 0o755); err != nil {
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

func (m *Manager) clawDir(clawID string) string {
	return filepath.Join(m.root, clawID)
}

func (m *Manager) lockPath(clawID string) string {
	return filepath.Join(m.clawDir(clawID), lockFileName)
}

func (m *Manager) statePath(clawID string) string {
	return filepath.Join(m.clawDir(clawID), stateFileName)
}

func (m *Manager) mountPath(clawID string) string {
	return filepath.Join(m.clawDir(clawID), mountDirName)
}

func validateClawID(clawID string) error {
	if !clawIDPattern.MatchString(clawID) {
		return fmt.Errorf("invalid claw id %q", clawID)
	}
	return nil
}

func readState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, nil
		}
		return State{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state State
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	return state, nil
}

func writeState(path string, state State) error {
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

type noopMounter struct{}

func (noopMounter) MountReadOnly(context.Context, string, string) error {
	return nil
}

func (noopMounter) Unmount(context.Context, string) error {
	return nil
}

func (noopMounter) IsMounted(context.Context, string) (bool, error) {
	return false, nil
}

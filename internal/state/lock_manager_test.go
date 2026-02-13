package state

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireAndReleaseUpdateState(t *testing.T) {
	root := t.TempDir()
	locker := &fakeLocker{ok: true}
	manager := NewLockManager(root, locker)
	manager.now = func() time.Time { return time.Date(2026, time.February, 10, 0, 0, 0, 0, time.UTC) }

	if err := manager.Acquire(context.Background(), AcquireRequest{
		ClawID:     "demo-123",
		SourcePath: filepath.Join(root, "demo.clawbox"),
		InstanceID: "claw-001",
		PID:        4321,
	}); err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	state, err := manager.Inspect("demo-123")
	if err != nil {
		t.Fatalf("Inspect failed: %v", err)
	}
	if !state.Active {
		t.Fatalf("expected active state")
	}
	if state.InstanceID != "claw-001" {
		t.Fatalf("unexpected instance id: %q", state.InstanceID)
	}
	if state.PID != 4321 {
		t.Fatalf("unexpected pid: %d", state.PID)
	}

	if err := manager.Release(context.Background(), ReleaseRequest{ClawID: "demo-123"}); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	state, err = manager.Inspect("demo-123")
	if err != nil {
		t.Fatalf("Inspect failed: %v", err)
	}
	if state.Active {
		t.Fatalf("expected inactive state")
	}
	if state.InstanceID != "" || state.PID != 0 {
		t.Fatalf("expected cleared runtime fields, got %+v", state)
	}
}

func TestAcquireFailsWhenLockBusy(t *testing.T) {
	manager := NewLockManager(t.TempDir(), &fakeLocker{ok: false})
	err := manager.Acquire(context.Background(), AcquireRequest{ClawID: "demo-123"})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
}

func TestAcquireDoesNotFailOnStaleActiveState(t *testing.T) {
	root := t.TempDir()
	manager := NewLockManager(root, &fakeLocker{ok: true})

	if err := writeState(filepath.Join(root, "demo-123", stateFileName), LockState{Active: true}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	err := manager.Acquire(context.Background(), AcquireRequest{ClawID: "demo-123"})
	if err != nil {
		t.Fatalf("Acquire should succeed despite stale active=true, got %v", err)
	}
}

func TestAcquireDetectsSourceConflict(t *testing.T) {
	root := t.TempDir()
	manager := NewLockManager(root, &fakeLocker{ok: true})

	if err := writeState(filepath.Join(root, "demo-123", stateFileName), LockState{SourcePath: "/a/demo.clawbox"}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	err := manager.Acquire(context.Background(), AcquireRequest{ClawID: "demo-123", SourcePath: "/b/demo.clawbox"})
	if !errors.Is(err, ErrSourceConflict) {
		t.Fatalf("expected ErrSourceConflict, got %v", err)
	}
}

func TestAcquireReusesSameSourcePath(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "demo.clawbox")
	manager := NewLockManager(root, &fakeLocker{ok: true})

	if err := writeState(filepath.Join(root, "demo-123", stateFileName), LockState{SourcePath: source}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	if err := manager.Acquire(context.Background(), AcquireRequest{ClawID: "demo-123", SourcePath: source}); err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	state, err := manager.Inspect("demo-123")
	if err != nil {
		t.Fatalf("Inspect failed: %v", err)
	}
	if state.SourcePath != source {
		t.Fatalf("unexpected source path: got %q want %q", state.SourcePath, source)
	}
}

func TestWithInstanceLockAndAcquireWhileLocked(t *testing.T) {
	root := t.TempDir()
	locker := &fakeLocker{ok: true}
	manager := NewLockManager(root, locker)

	err := manager.WithInstanceLock("demo-123", func() error {
		if err := manager.AcquireWhileLocked(context.Background(), AcquireRequest{
			ClawID:     "demo-123",
			SourcePath: filepath.Join(root, "demo.clawbox"),
			InstanceID: "claw-001",
			PID:        1234,
		}); err != nil {
			return err
		}
		return manager.ReleaseWhileLocked(context.Background(), ReleaseRequest{ClawID: "demo-123"})
	})
	if err != nil {
		t.Fatalf("WithInstanceLock failed: %v", err)
	}

	state, err := manager.Inspect("demo-123")
	if err != nil {
		t.Fatalf("Inspect failed: %v", err)
	}
	if state.Active {
		t.Fatalf("expected inactive state after release")
	}
}

func TestWithInstanceLockFailsWhenBusy(t *testing.T) {
	manager := NewLockManager(t.TempDir(), &fakeLocker{ok: false})
	err := manager.WithInstanceLock("demo-123", func() error { return nil })
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
}

func TestFlockLockerContention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.flock")
	locker := NewFlockLocker()

	handleA, ok, err := locker.TryLock(path)
	if err != nil {
		t.Fatalf("first lock failed: %v", err)
	}
	if !ok {
		t.Fatal("expected first lock to succeed")
	}

	handleB, ok, err := locker.TryLock(path)
	if err != nil {
		t.Fatalf("second lock errored: %v", err)
	}
	if ok || handleB != nil {
		t.Fatal("expected second lock to fail while first held")
	}

	if err := handleA.Unlock(); err != nil {
		t.Fatalf("unlock failed: %v", err)
	}

	handleC, ok, err := locker.TryLock(path)
	if err != nil {
		t.Fatalf("third lock errored: %v", err)
	}
	if !ok {
		t.Fatal("expected third lock to succeed after unlock")
	}
	if err := handleC.Unlock(); err != nil {
		t.Fatalf("unlock failed: %v", err)
	}
}

type fakeLocker struct {
	ok  bool
	err error
}

func (locker *fakeLocker) TryLock(string) (LockHandle, bool, error) {
	if locker.err != nil {
		return nil, false, locker.err
	}
	if !locker.ok {
		return nil, false, nil
	}
	return fakeLockHandle{}, true, nil
}

type fakeLockHandle struct{}

func (fakeLockHandle) Unlock() error {
	return nil
}

package mount

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
	mounter := &fakeMounter{isMounted: false}
	manager := NewManager(root, locker, mounter)
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
	if mounter.mountCalls != 1 {
		t.Fatalf("unexpected mount calls: %d", mounter.mountCalls)
	}

	if err := manager.Release(context.Background(), ReleaseRequest{ClawID: "demo-123", Unmount: true}); err != nil {
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
	if mounter.unmountCalls != 1 {
		t.Fatalf("unexpected unmount calls: %d", mounter.unmountCalls)
	}
}

func TestAcquireFailsWhenLockBusy(t *testing.T) {
	manager := NewManager(t.TempDir(), &fakeLocker{ok: false}, &fakeMounter{})
	err := manager.Acquire(context.Background(), AcquireRequest{ClawID: "demo-123"})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
}

func TestAcquireDoesNotFailOnStaleActiveState(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root, &fakeLocker{ok: true}, &fakeMounter{})

	if err := writeState(filepath.Join(root, "demo-123", stateFileName), State{Active: true}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	err := manager.Acquire(context.Background(), AcquireRequest{ClawID: "demo-123"})
	if err != nil {
		t.Fatalf("Acquire should succeed despite stale active=true, got %v", err)
	}
}

func TestAcquireDetectsMountConflict(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root, &fakeLocker{ok: true}, &fakeMounter{isMounted: true})

	if err := writeState(filepath.Join(root, "demo-123", stateFileName), State{SourcePath: "/a/demo.clawbox"}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	err := manager.Acquire(context.Background(), AcquireRequest{ClawID: "demo-123", SourcePath: "/b/demo.clawbox"})
	if !errors.Is(err, ErrMountConflict) {
		t.Fatalf("expected ErrMountConflict, got %v", err)
	}
}

func TestAcquireReusesMountedSourceWithoutRemount(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "demo.clawbox")
	manager := NewManager(root, &fakeLocker{ok: true}, &fakeMounter{isMounted: true})

	if err := writeState(filepath.Join(root, "demo-123", stateFileName), State{SourcePath: source}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	if err := manager.Acquire(context.Background(), AcquireRequest{ClawID: "demo-123", SourcePath: source}); err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	mounter := manager.mounter.(*fakeMounter)
	if mounter.mountCalls != 0 {
		t.Fatalf("expected no remount when already mounted with same source, got %d", mounter.mountCalls)
	}
}

func TestWithInstanceLockAndAcquireWhileLocked(t *testing.T) {
	root := t.TempDir()
	locker := &fakeLocker{ok: true}
	mounter := &fakeMounter{isMounted: false}
	manager := NewManager(root, locker, mounter)

	err := manager.WithInstanceLock("demo-123", func() error {
		if err := manager.AcquireWhileLocked(context.Background(), AcquireRequest{
			ClawID:     "demo-123",
			SourcePath: filepath.Join(root, "demo.clawbox"),
			InstanceID: "claw-001",
			PID:        1234,
		}); err != nil {
			return err
		}
		return manager.ReleaseWhileLocked(context.Background(), ReleaseRequest{ClawID: "demo-123", Unmount: true})
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
	if mounter.mountCalls != 1 {
		t.Fatalf("expected exactly one mount call, got %d", mounter.mountCalls)
	}
	if mounter.unmountCalls != 1 {
		t.Fatalf("expected exactly one unmount call, got %d", mounter.unmountCalls)
	}
}

func TestWithInstanceLockFailsWhenBusy(t *testing.T) {
	manager := NewManager(t.TempDir(), &fakeLocker{ok: false}, &fakeMounter{})
	err := manager.WithInstanceLock("demo-123", func() error { return nil })
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
}

func TestRecoverResetsStateAndUnmounts(t *testing.T) {
	root := t.TempDir()
	mounter := &fakeMounter{isMounted: true}
	manager := NewManager(root, &fakeLocker{ok: true}, mounter)

	if err := writeState(filepath.Join(root, "demo-123", stateFileName), State{Active: true, InstanceID: "x", PID: 1}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	if err := manager.Recover(context.Background(), "demo-123"); err != nil {
		t.Fatalf("Recover failed: %v", err)
	}

	state, err := manager.Inspect("demo-123")
	if err != nil {
		t.Fatalf("Inspect failed: %v", err)
	}
	if state.Active {
		t.Fatalf("expected inactive state after recover")
	}
	if mounter.unmountCalls != 1 {
		t.Fatalf("expected one unmount call, got %d", mounter.unmountCalls)
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

type fakeMounter struct {
	isMounted    bool
	mountCalls   int
	unmountCalls int
	mountErr     error
	unmountErr   error
	isMountedErr error
}

func (mounter *fakeMounter) MountReadOnly(context.Context, string, string) error {
	mounter.mountCalls++
	return mounter.mountErr
}

func (mounter *fakeMounter) Unmount(context.Context, string) error {
	mounter.unmountCalls++
	if mounter.unmountErr == nil {
		mounter.isMounted = false
	}
	return mounter.unmountErr
}

func (mounter *fakeMounter) IsMounted(context.Context, string) (bool, error) {
	if mounter.isMountedErr != nil {
		return false, mounter.isMountedErr
	}
	return mounter.isMounted, nil
}

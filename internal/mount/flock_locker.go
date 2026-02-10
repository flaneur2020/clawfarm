package mount

import "github.com/gofrs/flock"

type FlockLocker struct{}

func NewFlockLocker() *FlockLocker {
	return &FlockLocker{}
}

func (locker *FlockLocker) TryLock(path string) (LockHandle, bool, error) {
	fileLock := flock.New(path)
	ok, err := fileLock.TryLock()
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return flockLockHandle{fileLock: fileLock}, true, nil
}

type flockLockHandle struct {
	fileLock *flock.Flock
}

func (handle flockLockHandle) Unlock() error {
	return handle.fileLock.Unlock()
}

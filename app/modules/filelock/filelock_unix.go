//go:build !windows

package filelock

import (
	"errors"
	"os"
	"syscall"
)

// TryLock takes an exclusive, non-blocking flock on f (ErrHeld when
// another open file holds it).
func TryLock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrHeld
	}
	return err
}

// Unlock releases the flock on f.
func Unlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

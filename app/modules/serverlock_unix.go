//go:build !windows

package modules

import (
	"errors"
	"os"
	"syscall"
)

// lockFile takes an exclusive, non-blocking flock on f (errLockHeld when
// another process holds it).
func lockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errLockHeld
	}
	return err
}

// unlockFile releases the flock on f.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

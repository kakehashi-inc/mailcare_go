//go:build windows

package fileinuse

import (
	"errors"

	"golang.org/x/sys/windows"
)

// InUse reports whether err is the failure Windows gives for a file another
// process has open: a sharing or lock violation, or access denied for the
// removal or the rename of a file some other process has open. Access
// denied is also the answer for a file nobody has open (its attributes or
// permissions forbid the change): when every file the error names can be
// opened without sharing, nobody has it open and the failure is not of
// this kind (it is returned instead of waited out).
func InUse(err error) bool {
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return true
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return false
	}
	paths := errorPaths(err)
	if len(paths) == 0 {
		return true
	}
	for _, p := range paths {
		if openedElsewhere(p) {
			return true
		}
	}
	return false
}

// openedElsewhere reports whether another process has the file open: an
// open without sharing fails with a sharing violation.
func openedElsewhere(path string) bool {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return errors.Is(err, windows.ERROR_SHARING_VIOLATION)
	}
	_ = windows.CloseHandle(h)
	return false
}

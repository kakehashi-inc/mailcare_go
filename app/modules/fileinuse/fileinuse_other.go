//go:build !windows

package fileinuse

// InUse reports whether err is the failure of a file operation because
// another process has the file open; outside Windows an open file can be
// removed and renamed, so no failure is.
func InUse(error) bool { return false }

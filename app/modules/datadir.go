package modules

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// dataDirOverride is set from the --data-dir flag (see SetDataDir).
var dataDirOverride string

// SetDataDir overrides the data directory for this process. An empty value
// keeps the default (next to the executable).
func SetDataDir(dir string) {
	dataDirOverride = dir
}

// DataDir returns the application data directory as an absolute, cleaned
// path.
//
// By default this is "<dir of executable>/data" so the data lives next to the
// binary. It can be overridden with the --data-dir flag. The absolute form is
// what the server reports on /control/status and what the CLI compares its
// own directory with (SameDataDir) before handing jobs to a running server.
func DataDir() (string, error) {
	dir := dataDirOverride
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(filepath.Dir(exe), "data")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return abs, nil
}

// SameDataDir reports whether two data directory paths name the same
// directory: both are made absolute and cleaned, symbolic links are resolved
// when the path exists, and on Windows the comparison ignores case. An empty
// path never matches (a server that does not report its directory is not
// taken for one serving ours).
func SameDataDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ca, cb := canonicalDir(a), canonicalDir(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ca, cb)
	}
	return ca == cb
}

// canonicalDir normalizes a directory path for comparison.
func canonicalDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return filepath.Clean(dir)
}

// EnsureDataDir creates the data directory if it does not exist.
func EnsureDataDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// MailsDir returns the directory holding the raw mail files, their body
// sections and the per-mailbox index databases (<data>/mails).
func MailsDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, MailsDirName), nil
}

// AgentDir returns the directory holding the agent workspaces (<data>/agent).
// Together with the master database and MailsDir it is everything the data
// directory contains.
func AgentDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, AgentDirName), nil
}

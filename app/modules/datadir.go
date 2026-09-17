package modules

import (
	"os"
	"path/filepath"
)

// dataDirOverride is set from the --data-dir flag (see SetDataDir).
var dataDirOverride string

// SetDataDir overrides the data directory for this process. An empty value
// keeps the default (next to the executable).
func SetDataDir(dir string) {
	dataDirOverride = dir
}

// DataDir returns the application data directory.
//
// By default this is "<dir of executable>/data" so the data lives next to the
// binary. It can be overridden with the --data-dir flag.
func DataDir() (string, error) {
	if dataDirOverride != "" {
		return dataDirOverride, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "data"), nil
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

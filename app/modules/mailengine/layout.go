package mailengine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Directory layout of the raw files of one mailbox (design 3 / 3.2):
//
//	<mailsRoot>/<address>/<YYYY>/<MM>/<key>.eml, <key>-<n>.txt, <key>-<n>.html
//
// The year and month are those of the message key (the message date in
// UTC), see MessageDir.

// yearDirLen and monthDirLen are the name lengths of the year and month
// directories.
const (
	yearDirLen  = 4
	monthDirLen = 2
)

// isDigits reports whether s has exactly n ASCII digits.
func isDigits(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < n; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// walkMailboxFiles calls fn for every regular file in the <YYYY>/<MM>
// directories of the mailbox directory dir, with the directory that holds
// the file. Other directories and files are skipped. A missing directory is
// not an error.
func walkMailboxFiles(dir string, fn func(sub string, e os.DirEntry)) error {
	var walk func(sub string, depth int) error
	walk = func(sub string, depth int) error {
		entries, err := os.ReadDir(sub)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("read %s: %w", sub, err)
		}
		for _, e := range entries {
			switch {
			case e.Type().IsRegular() && depth == 2:
				fn(sub, e)
			case e.IsDir() && depth == 0 && isDigits(e.Name(), yearDirLen),
				e.IsDir() && depth == 1 && isDigits(e.Name(), monthDirLen):
				if err := walk(filepath.Join(sub, e.Name()), depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(dir, 0)
}

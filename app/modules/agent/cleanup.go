package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mailcare/app/modules/mailengine"
)

// CleanupWorkspaces removes the run directories of one mailbox
// (<agentRoot>/<address>/<group_key>/<report_id>/) whose modification time
// is older than keep, and then every group directory that is left empty.
// The rule is applied by directory age alone, so the directories of groups
// that no longer exist in the index expire the same way. A missing address
// directory is not an error. Entries that are not directories are left
// alone. It returns how many run directories were removed; when some entry
// could not be removed the others are still processed and the errors are
// returned joined.
func CleanupWorkspaces(agentRoot, address string, keep time.Duration) (removed int, err error) {
	if agentRoot == "" {
		return 0, errors.New("agent: no workspace root")
	}
	addressDir := filepath.Join(agentRoot, mailengine.SanitizeAddress(address))
	groups, rerr := os.ReadDir(addressDir)
	if rerr != nil {
		if errors.Is(rerr, os.ErrNotExist) {
			return 0, nil
		}
		return 0, rerr
	}
	cutoff := time.Now().Add(-keep)
	var errs []error
	for _, g := range groups {
		if !g.IsDir() {
			continue
		}
		groupDir := filepath.Join(addressDir, g.Name())
		runs, rerr := os.ReadDir(groupDir)
		if rerr != nil {
			errs = append(errs, rerr)
			continue
		}
		remaining := 0
		for _, r := range runs {
			if !r.IsDir() {
				remaining++
				continue
			}
			runDir := filepath.Join(groupDir, r.Name())
			info, ierr := r.Info()
			if ierr != nil {
				errs = append(errs, ierr)
				remaining++
				continue
			}
			if !info.ModTime().Before(cutoff) {
				remaining++
				continue
			}
			if rmErr := os.RemoveAll(runDir); rmErr != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", runDir, rmErr))
				remaining++
				continue
			}
			removed++
		}
		if remaining == 0 {
			if rmErr := os.Remove(groupDir); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("remove %s: %w", groupDir, rmErr))
			}
		}
	}
	return removed, errors.Join(errs...)
}

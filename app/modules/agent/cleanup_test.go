package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeRunDir creates <agentRoot>/<address>/<group>/<run>/RESULT.log and sets
// the directory's modification time to age ago.
func makeRunDir(t *testing.T, agentRoot, address, group, run string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(agentRoot, address, group, run)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ResultFileName), []byte("Result: Success\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(dir, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return dir
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestCleanupWorkspacesRemovesExpiredRunsAndEmptyGroups(t *testing.T) {
	agentRoot := t.TempDir()
	address := "bounce@example.com"
	keep := 30 * 24 * time.Hour
	// Group A: one expired run and one recent run -> the group stays.
	expiredA := makeRunDir(t, agentRoot, address, "aaaaaaaaaaaaaaaa", "1", keep+time.Hour)
	recentA := makeRunDir(t, agentRoot, address, "aaaaaaaaaaaaaaaa", "2", keep-time.Hour)
	// Group B: only expired runs (a group that may no longer exist in the
	// index) -> the runs and the group directory go.
	expiredB1 := makeRunDir(t, agentRoot, address, "bbbbbbbbbbbbbbbb", "3", 2*keep)
	expiredB2 := makeRunDir(t, agentRoot, address, "bbbbbbbbbbbbbbbb", "4", keep+time.Minute)
	// Group C: a stray file next to an expired run keeps the group directory.
	expiredC := makeRunDir(t, agentRoot, address, "cccccccccccccccc", "5", 3*keep)
	stray := filepath.Join(agentRoot, address, "cccccccccccccccc", "notes.txt")
	if err := os.WriteFile(stray, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Another address is never touched.
	otherExpired := makeRunDir(t, agentRoot, "other@example.com", "dddddddddddddddd", "6", 3*keep)

	removed, err := CleanupWorkspaces(agentRoot, address, keep)
	if err != nil {
		t.Fatalf("CleanupWorkspaces: %v", err)
	}
	if removed != 4 {
		t.Errorf("removed %d run directories, want 4", removed)
	}
	for _, gone := range []string{expiredA, expiredB1, expiredB2, expiredC, filepath.Dir(expiredB1)} {
		if exists(gone) {
			t.Errorf("%s should have been removed", gone)
		}
	}
	for _, kept := range []string{recentA, filepath.Dir(recentA), stray, filepath.Dir(stray), otherExpired, filepath.Join(agentRoot, address)} {
		if !exists(kept) {
			t.Errorf("%s should have been kept", kept)
		}
	}
	// A second pass finds nothing to do.
	removed, err = CleanupWorkspaces(agentRoot, address, keep)
	if err != nil || removed != 0 {
		t.Errorf("second pass: removed %d, %v", removed, err)
	}
}

func TestCleanupWorkspacesMissingAddressAndSanitizedName(t *testing.T) {
	agentRoot := t.TempDir()
	if removed, err := CleanupWorkspaces(agentRoot, "nobody@example.com", time.Hour); err != nil || removed != 0 {
		t.Errorf("missing address directory: removed %d, %v", removed, err)
	}
	if _, err := CleanupWorkspaces("", "nobody@example.com", time.Hour); err == nil {
		t.Error("an empty root must be refused")
	}
	// The address directory is the sanitized address, as AnalyzeGroup makes it.
	address := "we ird<addr>@example.com"
	run := makeRunDir(t, agentRoot, "we_ird_addr_@example.com", "eeeeeeeeeeeeeeee", "7", 48*time.Hour)
	if got := RunDir(agentRoot, address, "eeeeeeeeeeeeeeee", 7); got != run {
		t.Fatalf("RunDir = %s, want %s", got, run)
	}
	removed, err := CleanupWorkspaces(agentRoot, address, 24*time.Hour)
	if err != nil || removed != 1 || exists(run) {
		t.Errorf("sanitized address: removed %d, %v, exists %v", removed, err, exists(run))
	}
}

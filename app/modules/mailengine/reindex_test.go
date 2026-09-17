package mailengine

import (
	"context"
	"errors"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
)

// syntheticUID is the UID sourceForKey derives from a key.
func syntheticUID(key string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return h.Sum32()
}

// seedRawWithIdentity writes a raw file under key and, when the identity is
// given, an index row that carries that identity for the key (what the
// previous index knew about it).
func seedRawWithIdentity(t *testing.T, root, address, key string, raw []byte, identity *Source) {
	t.Helper()
	dir := MailboxDir(root, address)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, key+".eml"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if identity == nil {
		return
	}
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := &models.Message{MessageKey: key, Folder: identity.Folder, UIDValidity: identity.UIDValidity, UID: identity.UID,
		Date: time.Now().UTC(), FetchedAt: time.Now().UTC()}
	if err := models.InsertMessage(db, m); err != nil {
		t.Fatal(err)
	}
}

// TestReindexSyntheticUIDCollision checks that a raw file whose synthetic
// identity (UIDVALIDITY 0, UID = FNV-32 of the key) collides with an
// indexed row is stored under the next free UID, and that when the retries
// are exhausted the file is reported, counted as skipped and the rest of
// the index is still built.
func TestReindexSyntheticUIDCollision(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	raw := readSample(t, "normal.eml")
	newKey := "20260901-120000_bbbbbbbbbbbb" // unknown to the previous index: synthetic identity
	uid := syntheticUID(newKey)
	// Three carried rows occupy the synthetic UID of newKey and the two
	// after it.
	occupied := []string{"20260901-120000_aaaaaaaaaaa1", "20260901-120000_aaaaaaaaaaa2", "20260901-120000_aaaaaaaaaaa3"}
	for i, key := range occupied {
		seedRawWithIdentity(t, root, address, key, raw, &Source{Folder: defaultFolder, UIDValidity: 0, UID: uid + uint32(i)})
	}
	seedRawWithIdentity(t, root, address, newKey, raw, nil)

	var lines []string
	res, err := Reindex(context.Background(), root, address, func(m string) { lines = append(lines, m) })
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if res.Messages != 4 || res.Skipped != 0 {
		t.Fatalf("result = %+v, want 4 messages and nothing skipped", res)
	}
	db := mustOpenIndex(t, root, address)
	m, err := models.GetMessageByKey(db, newKey)
	if err != nil {
		t.Fatal(err)
	}
	if m.UIDValidity != 0 || m.UID != uid+3 || m.Folder != defaultFolder {
		t.Errorf("colliding file stored as (%s, %d, %d), want (INBOX, 0, %d)", m.Folder, m.UIDValidity, m.UID, uid+3)
	}
	for i, key := range occupied {
		if c, err := models.GetMessageByKey(db, key); err != nil || c.UID != uid+uint32(i) || c.UIDValidity != 0 {
			t.Errorf("carried row %s = %+v (err %v)", key, c, err)
		}
	}
	db.Close()

	// With fewer retries than colliding rows the file is left out, reported
	// and counted; the other files are indexed and the index is installed.
	saved := maxSyntheticUIDRetries
	maxSyntheticUIDRetries = 2
	defer func() { maxSyntheticUIDRetries = saved }()
	// Rebuild from the previous state: newKey now carries (0, uid+3), so
	// re-seed the old index with the three blockers only.
	root = t.TempDir()
	for i, key := range occupied {
		seedRawWithIdentity(t, root, address, key, raw, &Source{Folder: defaultFolder, UIDValidity: 0, UID: uid + uint32(i)})
	}
	seedRawWithIdentity(t, root, address, newKey, raw, nil)
	lines = nil
	res, err = Reindex(context.Background(), root, address, func(m string) { lines = append(lines, m) })
	if err != nil {
		t.Fatalf("reindex with exhausted retries: %v", err)
	}
	if res.Messages != 3 || res.Skipped != 1 {
		t.Errorf("result = %+v, want 3 messages and 1 skipped", res)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "error: skipping "+newKey) || !strings.Contains(joined, "1 skipped") {
		t.Errorf("progress lacks the skip report:\n%s", joined)
	}
	db = mustOpenIndex(t, root, address)
	defer db.Close()
	if _, err := models.GetMessageByKey(db, newKey); err == nil {
		t.Error("the skipped file was indexed after all")
	}
	if n := countRows(t, db, "messages"); n != 3 {
		t.Errorf("%d messages indexed, want 3", n)
	}
}

// TestRetryFileOp checks that a file operation is retried until it succeeds
// within the timeout and gives up with the last error afterwards.
func TestRetryFileOp(t *testing.T) {
	savedInterval, savedTimeout := fileRetryInterval, fileRetryTimeout
	fileRetryInterval, fileRetryTimeout = time.Millisecond, 200*time.Millisecond
	defer func() { fileRetryInterval, fileRetryTimeout = savedInterval, savedTimeout }()

	calls := 0
	err := retryFileOp(func() error {
		calls++
		if calls < 3 {
			return errors.New("still open")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Errorf("retry: err %v after %d calls, want success at the third", err, calls)
	}
	calls = 0
	sentinel := errors.New("never")
	start := time.Now()
	if err := retryFileOp(func() error { calls++; return sentinel }); !errors.Is(err, sentinel) {
		t.Errorf("exhausted retry returned %v, want the last error", err)
	}
	if calls < 2 || time.Since(start) < fileRetryTimeout {
		t.Errorf("gave up after %d calls in %s", calls, time.Since(start))
	}
	// The swap itself works on real files (a missing side file is fine).
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "a.sqlite.rebuild"), filepath.Join(dir, "a.sqlite")
	for _, p := range []string{src, src + "-wal", dst, dst + "-wal", dst + "-shm"} {
		if err := os.WriteFile(p, []byte(p), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := swapIndexFiles(src, dst); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != src {
		t.Errorf("dst = %q (err %v), want the rebuilt file", data, err)
	}
	if data, err := os.ReadFile(dst + "-wal"); err != nil || string(data) != src+"-wal" {
		t.Errorf("dst-wal = %q (err %v), want the rebuilt side file", data, err)
	}
	for _, p := range []string{src, src + "-wal", dst + "-shm"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists (err %v)", p, err)
		}
	}
}

// TestRemoveStaleTempFiles checks that only the temporary files of atomic
// writes older than StaleTempFileAge are removed: fresh ones (a write in
// progress) and regular files stay.
func TestRemoveStaleTempFiles(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	dir := MailboxDir(root, address)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-StaleTempFileAge - time.Hour)
	stale := []string{".20260901-120000_aaaaaaaaaaaa.eml.123456.tmp", ".20260901-120000_aaaaaaaaaaaa-1.txt.7.tmp"}
	kept := []string{".20260901-120000_bbbbbbbbbbbb.eml.999.tmp", "20260901-120000_aaaaaaaaaaaa.eml", "20260901-120000_aaaaaaaaaaaa-1.txt", ".hidden"}
	for _, name := range append(append([]string{}, stale...), kept...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range stale {
		if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	// A regular file that is old is not a temporary file.
	if err := os.Chtimes(filepath.Join(dir, kept[1]), old, old); err != nil {
		t.Fatal(err)
	}
	var lines []string
	n, err := RemoveStaleTempFiles(root, address, func(m string) { lines = append(lines, m) })
	if err != nil || n != 2 {
		t.Fatalf("removed %d (err %v), want 2", n, err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "2 stale temporary file") {
		t.Errorf("progress = %v", lines)
	}
	for _, name := range stale {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s not removed (err %v)", name, err)
		}
	}
	for _, name := range kept {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s removed: %v", name, err)
		}
	}
	// Nothing to do: no line, no error; a missing directory is fine too.
	lines = nil
	if n, err := RemoveStaleTempFiles(root, address, func(m string) { lines = append(lines, m) }); n != 0 || err != nil || len(lines) != 0 {
		t.Errorf("second run: %d, %v, %v", n, err, lines)
	}
	if n, err := RemoveStaleTempFiles(root, "nobody@example.jp", nil); n != 0 || err != nil {
		t.Errorf("missing directory: %d, %v", n, err)
	}
}

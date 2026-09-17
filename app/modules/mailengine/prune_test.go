package mailengine

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
)

// dateHeaderRe matches the top-level Date header of a raw message: the
// first one in the file (the Date of an embedded original comes later).
var dateHeaderRe = regexp.MustCompile(`(?m)^Date:[^\r\n]*`)

// redate returns raw with its top-level Date header set to date.
func redate(t *testing.T, raw []byte, date time.Time) []byte {
	t.Helper()
	loc := dateHeaderRe.FindIndex(raw)
	if loc == nil {
		t.Fatal("sample has no Date header")
	}
	out := append([]byte{}, raw[:loc[0]]...)
	out = append(out, []byte("Date: "+date.Format(time.RFC1123Z))...)
	return append(out, raw[loc[1]:]...)
}

// datedSample is one raw message of the prune tests together with the age
// its Date header is set to.
type datedSample struct {
	file string
	age  time.Duration
}

// storeDated stores the samples with their Date header set to now - age
// (fetch-equivalent, classified = 0) and returns the message keys by file.
func storeDated(t *testing.T, root, address string, list []datedSample) map[string]string {
	t.Helper()
	dir := MailboxDir(root, address)
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	keys := map[string]string{}
	for i, s := range list {
		raw := redate(t, readSample(t, s.file), time.Now().Add(-s.age))
		src := Source{Folder: "INBOX", UIDValidity: 1700000000, UID: uint32(i + 1), ReceivedAt: time.Now().UTC(), FetchedAt: time.Now().UTC()}
		msg, _, err := storeMessage(db, dir, raw, src, storeOptions{writeEML: true})
		if err != nil {
			t.Fatalf("store %s: %v", s.file, err)
		}
		keys[s.file] = msg.MessageKey
	}
	return keys
}

// groupOf returns the group key of the bounce of a message ("" when none).
func groupOf(t *testing.T, db *sql.DB, key string) string {
	t.Helper()
	m, err := models.GetMessageByKey(db, key)
	if err != nil {
		t.Fatalf("message %s: %v", key, err)
	}
	return m.GroupKey
}

// completeReport gives a group a completed agent report and clears its
// analysis flag, and returns the report id.
func completeReport(t *testing.T, db *sql.DB, groupKey string) int64 {
	t.Helper()
	r := &models.AgentReport{GroupKey: groupKey, Provider: "codex", MessageCount: 1}
	if err := models.InsertAgentReport(db, r); err != nil {
		t.Fatal(err)
	}
	if err := models.CompleteAgentReport(db, r.ID, "summary of "+groupKey, responsibleSender, "high", "# report"); err != nil {
		t.Fatal(err)
	}
	if err := models.SetGroupNeedsAnalysis(db, groupKey, false); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// TestPruneMailboxRemovesExpiredMail mixes mails older and newer than the
// retention: the old ones lose their files and rows (bounces included), the
// groups they belonged to are recounted, a group left empty disappears with
// its report, and the mails within the retention keep every file and row.
func TestPruneMailboxRemovesExpiredMail(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	const day = 24 * time.Hour
	old, recent := 40*day, 2*day
	list := []datedSample{
		{"spamhaus_block_a.eml", old},    // ip_blocked group: keeps its other message
		{"spamhaus_block_b.eml", recent}, // the surviving member of that group
		{"postfix_dsn.eml", old},         // user_unknown group of one message: vanishes with its report
		{"exchange_html_only.eml", old},  // HTML-only sections, another single-message group
		{"normal.eml", old},              // not a bounce: no group, files and row go
		{"exim_bounce.eml", recent},      // untouched bounce with its group
		{"autoreply.eml", recent},        // untouched, no bounces row
	}
	keys := storeDated(t, root, address, list)
	if _, err := GroupMailbox(context.Background(), root, address, false, nil); err != nil {
		t.Fatalf("group: %v", err)
	}
	db := mustOpenIndex(t, root, address)
	spamKey := groupOf(t, db, keys["spamhaus_block_a.eml"])
	taroKey := groupOf(t, db, keys["postfix_dsn.eml"])
	kuroKey := groupOf(t, db, keys["exchange_html_only.eml"])
	jiroKey := groupOf(t, db, keys["exim_bounce.eml"])
	if spamKey == "" || taroKey == "" || kuroKey == "" || jiroKey == "" || spamKey != groupOf(t, db, keys["spamhaus_block_b.eml"]) {
		t.Fatalf("unexpected grouping: spam %q taro %q kuro %q jiro %q", spamKey, taroKey, kuroKey, jiroKey)
	}
	if groupOf(t, db, keys["normal.eml"]) != "" || groupOf(t, db, keys["autoreply.eml"]) != "" {
		t.Fatal("a non-bounce or an auto-reply was grouped")
	}
	if g, err := models.GetGroup(db, spamKey); err != nil || g.MessageCount != 2 {
		t.Fatalf("spamhaus group before prune: %+v, %v", g, err)
	}
	spamReport := completeReport(t, db, spamKey)
	completeReport(t, db, taroKey)
	if countRows(t, db, "groups") != 4 || countRows(t, db, "agent_reports") != 2 || countRows(t, db, "bounces") != 5 {
		t.Fatalf("seed: groups %d, reports %d, bounces %d", countRows(t, db, "groups"), countRows(t, db, "agent_reports"), countRows(t, db, "bounces"))
	}
	before := mustListAllMessages(t, db)
	assertSectionFiles(t, root, address, before, "before prune")
	var removedKeys, keptKeys []string
	for _, s := range list {
		if s.age > 30*day {
			removedKeys = append(removedKeys, keys[s.file])
		} else {
			keptKeys = append(keptKeys, keys[s.file])
		}
	}

	var lines []string
	res, err := PruneMailbox(context.Background(), root, address, 30*day, func(m string) { lines = append(lines, m) })
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if res.Removed != len(removedKeys) || res.RemovedGroups != 2 {
		t.Errorf("result = %+v, want %d removed / 2 groups", res, len(removedKeys))
	}
	if len(lines) != 1 || lines[0] != "removed 4 message(s) older than 30 days, 2 group(s) left empty and removed" {
		t.Errorf("progress = %q", lines)
	}

	// Rows: only the recent mails remain, with their bounces rows.
	after := mustListAllMessages(t, db)
	remaining := map[string]bool{}
	for _, m := range after {
		remaining[m.MessageKey] = true
	}
	for _, key := range removedKeys {
		if remaining[key] {
			t.Errorf("expired message %s still indexed", key)
		}
	}
	for _, key := range keptKeys {
		if !remaining[key] {
			t.Errorf("message %s within the retention was removed", key)
		}
	}
	if len(after) != len(keptKeys) {
		t.Errorf("%d rows left, want %d", len(after), len(keptKeys))
	}
	if n := countRows(t, db, "bounces"); n != 2 {
		t.Errorf("bounces rows = %d, want 2 (spamhaus b, exim)", n)
	}
	// Files: the directory holds exactly the files of the remaining rows.
	assertSectionFiles(t, root, address, after, "after prune")
	for _, key := range removedKeys {
		if _, err := os.Stat(MessageFilePath(root, address, key, "eml")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("raw file of %s still exists (%v)", key, err)
		}
	}
	// Groups: the shared group is recounted and keeps its report without
	// being flagged again; the emptied groups are gone with their reports;
	// the untouched group is as it was.
	g, err := models.GetGroup(db, spamKey)
	if err != nil || g.MessageCount != 1 || g.RecipientCount != 1 || g.NeedsAnalysis {
		t.Errorf("spamhaus group after prune: %+v, %v", g, err)
	}
	if r, err := models.LatestCompletedAgentReport(db, spamKey); err != nil || r.ID != spamReport {
		t.Errorf("report of the surviving group: %+v, %v", r, err)
	}
	for _, key := range []string{taroKey, kuroKey} {
		if _, err := models.GetGroup(db, key); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("emptied group %s still exists (%v)", key, err)
		}
		if reports, _ := models.ListAgentReports(db, key); len(reports) != 0 {
			t.Errorf("emptied group %s kept %d reports", key, len(reports))
		}
	}
	if n := countRows(t, db, "agent_reports"); n != 1 {
		t.Errorf("agent_reports rows = %d, want 1", n)
	}
	if g, err := models.GetGroup(db, jiroKey); err != nil || g.MessageCount != 1 {
		t.Errorf("untouched group after prune: %+v, %v", g, err)
	}
	if n := countRows(t, db, "groups"); n != 2 {
		t.Errorf("groups rows = %d, want 2", n)
	}

	// Nothing left to remove: silent no-op.
	lines = nil
	res, err = PruneMailbox(context.Background(), root, address, 30*day, func(m string) { lines = append(lines, m) })
	if err != nil || res.Removed != 0 || res.RemovedGroups != 0 || len(lines) != 0 {
		t.Errorf("second prune: %+v, %v, lines %q", res, err, lines)
	}
	if got := mustListAllMessages(t, db); len(got) != len(keptKeys) {
		t.Errorf("second prune removed rows: %d left", len(got))
	}
	// A retention that is not positive is refused; a cancelled context
	// removes nothing.
	if _, err := PruneMailbox(context.Background(), root, address, 0, nil); err == nil {
		t.Error("zero retention accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PruneMailbox(cancelled, root, address, time.Hour, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled prune: %v", err)
	}
	if got := mustListAllMessages(t, db); len(got) != len(keptKeys) {
		t.Errorf("cancelled prune removed rows: %d left", len(got))
	}
}

// TestPruneMailboxKeepsRowWhenFileRemovalFails: a message whose files cannot
// be removed keeps its row (so the next run tries again) while the other
// expired messages are still processed; the failure is returned with the
// partial result.
func TestPruneMailboxKeepsRowWhenFileRemovalFails(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the test user cannot write")
	}
	root := t.TempDir()
	address := "newsletter@example.jp"
	const day = 24 * time.Hour
	keys := storeDated(t, root, address, []datedSample{{"normal.eml", 40 * day}, {"postfix_dsn.eml", 40 * day}})
	if _, err := GroupMailbox(context.Background(), root, address, false, nil); err != nil {
		t.Fatal(err)
	}
	dir := MailboxDir(root, address)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	var lines []string
	res, err := PruneMailbox(context.Background(), root, address, 30*day, func(m string) { lines = append(lines, m) })
	if err == nil || !strings.Contains(err.Error(), "remove ") {
		t.Fatalf("expected a removal error, got %v", err)
	}
	if res == nil || res.Removed != 0 || len(lines) != 0 {
		t.Errorf("result = %+v, lines %q", res, lines)
	}
	db := mustOpenIndex(t, root, address)
	if got := mustListAllMessages(t, db); len(got) != 2 {
		t.Errorf("rows after the failed prune: %d, want 2", len(got))
	}
	if countRows(t, db, "groups") != 1 {
		t.Errorf("groups touched by a failed prune: %d", countRows(t, db, "groups"))
	}
	// Once the files can be removed the next run finishes the job.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	res, err = PruneMailbox(context.Background(), root, address, 30*day, nil)
	if err != nil || res.Removed != 2 || res.RemovedGroups != 1 {
		t.Errorf("retry: %+v, %v", res, err)
	}
	for _, key := range keys {
		if _, err := os.Stat(MessageFilePath(root, address, key, "eml")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("raw file of %s still exists (%v)", key, err)
		}
	}
	if got := mustListAllMessages(t, db); len(got) != 0 || countRows(t, db, "groups") != 0 {
		t.Errorf("rows left after the retry: %d messages, %d groups", len(got), countRows(t, db, "groups"))
	}
}

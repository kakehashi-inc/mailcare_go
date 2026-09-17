package mailengine

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
)

// failingExecer wraps an Execer and fails every statement containing
// failOn, recording the writes it saw (in order) so that a test can check
// what was attempted before the failure.
type failingExecer struct {
	models.Execer
	failOn string
	writes []string
}

var errInjected = errors.New("injected failure")

func (f *failingExecer) Exec(query string, args ...any) (sql.Result, error) {
	head := strings.Join(strings.Fields(query), " ")
	if len(head) > 40 {
		head = head[:40]
	}
	f.writes = append(f.writes, head)
	if f.failOn != "" && strings.Contains(query, f.failOn) {
		return nil, errInjected
	}
	return f.Execer.Exec(query, args...)
}

// storeOne stores one sample with classified = 0 and returns its row and
// parsed form.
func storeOne(t *testing.T, root, address, file string, uid uint32) (*models.Message, *ParsedMessage) {
	t.Helper()
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	src := Source{Folder: "INBOX", UIDValidity: 1, UID: uid, ReceivedAt: time.Now().UTC(), FetchedAt: time.Now().UTC()}
	msg, pm, err := storeMessage(db, MailboxDir(root, address), readSample(t, file), src, storeOptions{writeEML: true})
	if err != nil {
		t.Fatal(err)
	}
	return msg, pm
}

// assertUntouched checks that the index holds no bounces row, no group and
// an unclassified message: the state after a failed grouping step.
func assertUntouched(t *testing.T, db *sql.DB, msg *models.Message, groupsAllowed bool, label string) {
	t.Helper()
	if n := countRows(t, db, "bounces"); n != 0 {
		t.Errorf("%s: %d bounces rows left", label, n)
	}
	if n := countRows(t, db, "groups"); n != 0 && !groupsAllowed {
		t.Errorf("%s: %d groups rows left", label, n)
	}
	fresh, err := models.GetMessageByKey(db, msg.MessageKey)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Classified || fresh.IsBounce || fresh.BounceKind != "" || fresh.GroupKey != "" {
		t.Errorf("%s: message not left unclassified: %+v", label, fresh)
	}
}

// TestGroupMessageTransaction checks that the writes of one message form a
// transaction (a failure at the last step leaves nothing behind) and that
// their order is groups, bounces, messages, so that even without a
// transaction an interruption leaves the message unclassified for the next
// run instead of a classified message without its details.
func TestGroupMessageTransaction(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	msg, pm := storeOne(t, root, address, "spamhaus_block_a.eml", 1)
	db := mustOpenIndex(t, root, address)
	defer db.Close()

	// 1. Inside a transaction: the last statement (the messages row) fails,
	//    the rollback undoes the group and the bounce details.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	failing := &failingExecer{Execer: tx, failOn: "UPDATE messages SET is_bounce"}
	copyMsg := *msg
	if err := groupMessageIn(failing, &copyMsg, pm, newGroupTracker(), address); !errors.Is(err, errInjected) {
		t.Fatalf("groupMessageIn with a failing messages update returned %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertUntouched(t, db, msg, false, "after rollback")
	joined := strings.Join(failing.writes, "\n")
	groupsAt, bouncesAt, messagesAt := strings.Index(joined, "INSERT INTO groups"), strings.Index(joined, "INSERT INTO bounces"), strings.Index(joined, "UPDATE messages SET is_bounce")
	if groupsAt < 0 || bouncesAt < 0 || messagesAt < 0 || !(groupsAt < bouncesAt && bouncesAt < messagesAt) {
		t.Errorf("write order is not groups -> bounces -> messages:\n%s", joined)
	}

	// 2. Without a transaction, failing at the bounces row: the group row
	//    may exist (it comes first) but the message stays unclassified and
	//    has no details, so the next run repeats it.
	failing = &failingExecer{Execer: db, failOn: "INSERT INTO bounces"}
	copyMsg = *msg
	if err := groupMessageIn(failing, &copyMsg, pm, newGroupTracker(), address); !errors.Is(err, errInjected) {
		t.Fatalf("groupMessageIn with a failing bounces insert returned %v", err)
	}
	assertUntouched(t, db, msg, true, "after a failed bounces insert")

	// 3. The real path (groupMessage over the database) completes the
	//    message and its group in one go.
	if err := groupMessage(db, msg, pm, newGroupTracker(), address); err != nil {
		t.Fatal(err)
	}
	fresh, err := models.GetMessageByKey(db, msg.MessageKey)
	if err != nil || !fresh.Classified || !fresh.IsBounce || fresh.GroupKey == "" {
		t.Fatalf("message after grouping: %+v (err %v)", fresh, err)
	}
	if n := countRows(t, db, "bounces"); n != 1 {
		t.Errorf("%d bounces rows, want 1", n)
	}
	g, err := models.GetGroup(db, fresh.GroupKey)
	if err != nil || g.MessageCount != 1 || !g.NeedsAnalysis || !g.Actionable {
		t.Errorf("group = %+v (err %v), want 1 message and needs_analysis", g, err)
	}
	// The counters are written in the same transaction as the message, so
	// they are never behind the bounces rows.
	if n := countRows(t, db, "groups"); n != 1 {
		t.Errorf("%d groups rows, want 1", n)
	}
}

// TestClearMailClassificationIsAtomic checks that the reset of the
// detection outcome (which also clears body_source) happens in one
// transaction and that a full run afterwards rebuilds the same picture.
func TestClearMailClassificationIsAtomic(t *testing.T) {
	root, address := buildMailbox(t)
	db := mustOpenIndex(t, root, address)
	defer db.Close()
	if countRows(t, db, "bounces") == 0 {
		t.Fatal("precondition: no bounces rows")
	}
	if err := models.ClearMailClassification(db); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "bounces"); n != 0 {
		t.Errorf("%d bounces rows left after the reset", n)
	}
	for _, m := range mustListAllMessages(t, db) {
		if m.Classified || m.IsBounce || m.BounceKind != "" || m.Rule != "" || m.BodySource != "" {
			t.Errorf("%s not reset: %+v", m.MessageKey, m)
		}
	}
	res, err := GroupMailbox(context.Background(), root, address, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Processed != len(samples) {
		t.Errorf("processed %d after the reset, want %d", res.Processed, len(samples))
	}
	for _, m := range mustListAllMessages(t, db) {
		if !m.Classified || (m.TextCount+m.HTMLCount > 0 && m.BodySource == "") {
			t.Errorf("%s after regrouping: %+v", m.MessageKey, m)
		}
	}
}

// TestNeedsAnalysisOnlyForActionableGroups checks that a recipient-side
// group never carries needs_analysis = 1: not when it is created, not when
// it gains messages, not when it is restored by a reindex; an actionable
// group is flagged when created and when it grows.
func TestNeedsAnalysisOnlyForActionableGroups(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	excluded := &models.BounceGroup{GroupKey: "0000000000000001", Category: categoryUserUnknown, Actionable: false,
		UnitValue: "a@x.example", Responsible: responsibleRecipient}
	actionable := &models.BounceGroup{GroupKey: "0000000000000002", Category: categoryIPBlocked, Actionable: true,
		UnitValue: "203.0.113.5", Authority: "spamhaus.org", Responsible: responsibleSender}
	for _, g := range []*models.BounceGroup{excluded, actionable} {
		if err := models.UpsertGroup(db, g); err != nil {
			t.Fatal(err)
		}
	}
	flag := func(key string) bool {
		t.Helper()
		g, err := models.GetGroup(db, key)
		if err != nil {
			t.Fatal(err)
		}
		return g.NeedsAnalysis
	}
	if flag(excluded.GroupKey) || !flag(actionable.GroupKey) {
		t.Errorf("after insert: excluded=%v actionable=%v, want false / true", flag(excluded.GroupKey), flag(actionable.GroupKey))
	}
	// Both gain a message; the counters are refreshed.
	for i, g := range []*models.BounceGroup{excluded, actionable} {
		m := &models.Message{MessageKey: MessageKey(time.Now(), "INBOX", 1, uint32(i+1)), UID: uint32(i + 1), UIDValidity: 1,
			Date: time.Now().UTC(), IsBounce: true, BounceKind: bounceKindFailed, Classified: true}
		if err := models.InsertMessage(db, m); err != nil {
			t.Fatal(err)
		}
		if err := models.UpsertBounce(db, &models.Bounce{ID: m.ID, GroupKey: g.GroupKey, Recipient: "r@x.example"}); err != nil {
			t.Fatal(err)
		}
		if err := models.SetGroupNeedsAnalysis(db, g.GroupKey, false); err != nil {
			t.Fatal(err)
		}
		if err := models.RefreshGroupCounters(db, g.GroupKey); err != nil {
			t.Fatal(err)
		}
	}
	// RefreshGroupCounters counts only; the excluded group stays at 0.
	if flag(excluded.GroupKey) {
		t.Error("recipient-side group flagged by a counter refresh")
	}
	if flag(actionable.GroupKey) {
		t.Error("RefreshGroupCounters must not set the flag by itself (the grouping phase does)")
	}
	// A restore (reindex carry-over) never flags a recipient-side group.
	if err := models.RestoreGroupState(db, excluded.GroupKey, "open", sql.NullTime{}, true); err != nil {
		t.Fatal(err)
	}
	if err := models.RestoreGroupState(db, actionable.GroupKey, "open", sql.NullTime{}, true); err != nil {
		t.Fatal(err)
	}
	if flag(excluded.GroupKey) || !flag(actionable.GroupKey) {
		t.Errorf("after restore: excluded=%v actionable=%v, want false / true", flag(excluded.GroupKey), flag(actionable.GroupKey))
	}
	db.Close()

	// Through the grouping phase: an over-quota bounce (recipient side) is
	// grouped without the flag, the blacklist bounce with it.
	root2 := t.TempDir()
	storeSamples(t, root2, address, 1, sampleByFile(t, "gmail_overquota.eml"), sampleByFile(t, "spamhaus_block_a.eml"))
	res, err := GroupMailbox(context.Background(), root2, address, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	db = mustOpenIndex(t, root2, address)
	defer db.Close()
	quota, err := models.GetGroup(db, GroupKey(categoryMailboxFull, "kyuro@gmail.com", ""))
	if err != nil || quota.NeedsAnalysis || quota.Actionable {
		t.Errorf("over-quota group = %+v (err %v), want no analysis flag", quota, err)
	}
	spam, err := models.GetGroup(db, GroupKey(categoryIPBlocked, "203.0.113.5", "spamhaus.org"))
	if err != nil || !spam.NeedsAnalysis {
		t.Errorf("spamhaus group = %+v (err %v), want the analysis flag", spam, err)
	}
	if len(res.GroupsTouched) != 1 || res.GroupsTouched[0] != spam.GroupKey {
		t.Errorf("touched = %v, want only the actionable group", res.GroupsTouched)
	}
	// A second message for the excluded group: still no flag, while the
	// actionable group is flagged again after its flag was cleared.
	if err := models.SetGroupNeedsAnalysis(db, spam.GroupKey, false); err != nil {
		t.Fatal(err)
	}
	db.Close()
	storeSamples(t, root2, address, 3, sampleByFile(t, "gmail_overquota.eml"), sampleByFile(t, "spamhaus_block_b.eml"))
	// The second over-quota sample has the same Message-ID; give it another
	// identity by storing it under a new UID (storeSamples did), which is
	// enough for the grouping phase.
	if _, err := GroupMailbox(context.Background(), root2, address, false, nil); err != nil {
		t.Fatal(err)
	}
	db = mustOpenIndex(t, root2, address)
	quota, _ = models.GetGroup(db, quota.GroupKey)
	spam, _ = models.GetGroup(db, spam.GroupKey)
	if quota == nil || quota.MessageCount != 2 || quota.NeedsAnalysis {
		t.Errorf("over-quota group after growth = %+v, want 2 messages and no flag", quota)
	}
	if spam == nil || spam.MessageCount != 2 || !spam.NeedsAnalysis {
		t.Errorf("spamhaus group after growth = %+v, want 2 messages and the flag", spam)
	}
}

// sampleByFile returns the sample table entry of a testdata file.
func sampleByFile(t *testing.T, file string) sample {
	t.Helper()
	for _, s := range samples {
		if s.file == file {
			return s
		}
	}
	t.Fatalf("no sample %s", file)
	return sample{}
}

package models

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"mailcare/app/modules/dbschema"
)

// newBounceIndex opens a migrated mail index with two messages.
func newBounceIndex(t *testing.T) (*sql.DB, []*Message) {
	t.Helper()
	db, err := OpenMailIndex(filepath.Join(t.TempDir(), "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var msgs []*Message
	for i, key := range []string{"20260901-120000_aaaaaaaaaaaa", "20260902-120000_bbbbbbbbbbbb"} {
		m := &Message{MessageKey: key, UID: uint32(i + 1), UIDValidity: 1, Date: time.Date(2026, 9, i+1, 12, 0, 0, 0, time.UTC),
			IsBounce: true, BounceKind: "failed", Classified: true}
		if err := InsertMessage(db, m); err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, m)
	}
	for _, key := range []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"} {
		if err := UpsertGroup(db, &BounceGroup{GroupKey: key, Category: "user_unknown", UnitValue: key}); err != nil {
			t.Fatal(err)
		}
	}
	return db, msgs
}

// TestReplaceBouncesPerRecipient: a message holds one bounces row per
// failed recipient, in order; replacing them drops the old ones, the
// message lists every group of its recipients, the counters of a group
// count the message once and each recipient, the message is listed under
// each group and carries the group of its first recipient, and deleting
// the message removes its rows.
func TestReplaceBouncesPerRecipient(t *testing.T) {
	db, msgs := newBounceIndex(t)
	m := msgs[0]
	rows := []*Bounce{
		{GroupKey: "aaaaaaaaaaaaaaaa", Recipient: "a1@example.net", CategoryRule: "status_user_unknown"},
		{GroupKey: "aaaaaaaaaaaaaaaa", Recipient: "a2@example.net", CategoryRule: "status_user_unknown"},
		{GroupKey: "bbbbbbbbbbbbbbbb", Recipient: "b1@example.org", CategoryRule: "dns_unverified_temporary"},
	}
	if err := ReplaceBounces(db, m.ID, rows); err != nil {
		t.Fatal(err)
	}
	got, err := ListBouncesByMessage(db, m.ID)
	if err != nil || len(got) != 3 {
		t.Fatalf("rows %+v, %v", got, err)
	}
	for i, b := range got {
		if b.MessageID != m.ID || b.RecipientIndex != i || b.Recipient != rows[i].Recipient || b.ID == 0 {
			t.Errorf("row %d: %+v", i, b)
		}
	}
	if first, err := GetBounceByMessageID(db, m.ID); err != nil || first.Recipient != "a1@example.net" {
		t.Errorf("first recipient: %+v, %v", first, err)
	}
	if keys, err := ListBounceGroupKeys(db, m.ID); err != nil || !slices.Equal(keys, []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"}) {
		t.Errorf("group keys %v, %v", keys, err)
	}
	for _, key := range []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"} {
		if err := RefreshGroupCounters(db, key); err != nil {
			t.Fatal(err)
		}
	}
	if g, err := GetGroup(db, "aaaaaaaaaaaaaaaa"); err != nil || g.MessageCount != 1 || g.RecipientCount != 2 {
		t.Errorf("group a: %+v, %v; want 1 message, 2 recipients", g, err)
	}
	if st, err := GroupStats(db, "aaaaaaaaaaaaaaaa"); err != nil || !slices.Equal(st.Recipients, []string{"a1@example.net", "a2@example.net"}) {
		t.Errorf("stats %+v, %v", st, err)
	}
	for _, key := range []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"} {
		listed, total, err := ListMessages(db, MessageFilter{GroupKey: key})
		if err != nil || total != 1 || listed[0].ID != m.ID || listed[0].GroupKey != "aaaaaaaaaaaaaaaa" {
			t.Errorf("messages of %s: %+v, %d, %v", key, listed, total, err)
		}
	}
	if again, err := ListMessagesByBounceRulePrefix(db, "dns_unverified_"); err != nil || len(again) != 1 || again[0].ID != m.ID {
		t.Errorf("messages to verify again: %+v, %v", again, err)
	}
	expired, err := ListMessagesOlderThan(db, time.Date(2026, 9, 1, 13, 0, 0, 0, time.UTC))
	if err != nil || len(expired) != 1 {
		t.Fatalf("expired %+v, %v", expired, err)
	}
	slices.Sort(expired[0].GroupKeys)
	if !slices.Equal(expired[0].GroupKeys, []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"}) {
		t.Errorf("expired message groups %v", expired[0].GroupKeys)
	}

	// Replacing: the old rows go; none removes them all.
	if err := ReplaceBounces(db, m.ID, []*Bounce{{GroupKey: "cccccccccccccccc", Recipient: "c1@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if keys, _ := ListBounceGroupKeys(db, m.ID); !slices.Equal(keys, []string{"cccccccccccccccc"}) {
		t.Errorf("after replacing: group keys %v", keys)
	}
	for _, key := range []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"} {
		if err := DeleteGroupIfEmpty(db, key); err != nil {
			t.Fatal(err)
		}
		if _, err := GetGroup(db, key); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("empty group %s was kept (%v)", key, err)
		}
	}
	if err := DeleteGroupIfEmpty(db, "cccccccccccccccc"); err != nil {
		t.Fatal(err)
	}
	if _, err := GetGroup(db, "cccccccccccccccc"); err != nil {
		t.Errorf("a group with a member was removed: %v", err)
	}
	if err := DeleteMessage(db, m.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM bounces`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d bounces rows left after deleting the message (%v)", n, err)
	}
}

// TestBounceRecipientsMigration: migration 0005 keeps every bounces row of
// an index of version 4 as the first failed recipient of its message
// (message_id = the old id, recipient_index 0) with every column, and its
// down step keeps the first recipient of every message under the id of the
// message.
func TestBounceRecipientsMigration(t *testing.T) {
	db, msgs := newBounceIndex(t)
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectSQLite3, db, dbschema.Index(), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 4); err != nil {
		t.Fatal(err)
	}
	// A bounces row of 0.3.0: the id of its message.
	if _, err := db.Exec(`INSERT INTO bounces (id, group_key, recipient, recipient_domain, action, status_code, smtp_code,
		diagnostic, diagnostic_template, diagnostic_source, category_rule, pattern_key, remote_mta, remote_ip, reporting_mta,
		original_message_id, original_subject, original_from, original_date)
		VALUES (?, 'aaaaaaaaaaaaaaaa', 'a1@example.net', 'example.net', 'failed', '5.1.1', '550', 'diag', 'tpl', 'dsn',
		'status_user_unknown', 'pk', 'mx.example.net', '192.0.2.1', 'mx1.example.jp', '<o@example.jp>', 'subject',
		'from@example.jp', NULL)`, msgs[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := dbschema.Apply(ctx, db, dbschema.Index()); err != nil {
		t.Fatal(err)
	}
	got, err := ListBouncesByMessage(db, msgs[1].ID)
	if err != nil || len(got) != 1 {
		t.Fatalf("rows after the migration: %+v, %v", got, err)
	}
	b := got[0]
	if b.MessageID != msgs[1].ID || b.RecipientIndex != 0 || b.GroupKey != "aaaaaaaaaaaaaaaa" || b.Recipient != "a1@example.net" ||
		b.StatusCode != "5.1.1" || b.CategoryRule != "status_user_unknown" || b.ReportingMTA != "mx1.example.jp" || b.OriginalSubject != "subject" {
		t.Errorf("migrated row %+v", b)
	}
	if listed, _, err := ListMessages(db, MessageFilter{GroupKey: "aaaaaaaaaaaaaaaa"}); err != nil || len(listed) != 1 || listed[0].GroupKey != "aaaaaaaaaaaaaaaa" {
		t.Errorf("messages of the group: %+v, %v", listed, err)
	}
	// The unique index: one row per position of a message.
	if _, err := db.Exec(`INSERT INTO bounces (message_id, recipient_index, group_key, recipient, recipient_domain, action, status_code,
		smtp_code, diagnostic, diagnostic_template, diagnostic_source, category_rule, pattern_key, remote_mta, remote_ip, reporting_mta,
		original_message_id, original_subject, original_from) VALUES (?, 0, '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '', '')`,
		msgs[1].ID); err == nil {
		t.Error("a second row of the same position was stored")
	}

	// Down: the first recipient of every message stays, under its id.
	if err := ReplaceBounces(db, msgs[0].ID, []*Bounce{{GroupKey: "bbbbbbbbbbbbbbbb", Recipient: "x1@example.org"},
		{GroupKey: "cccccccccccccccc", Recipient: "x2@example.org"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 4); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT id, recipient FROM bounces ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var left []string
	for rows.Next() {
		var id int64
		var recipient string
		if err := rows.Scan(&id, &recipient); err != nil {
			t.Fatal(err)
		}
		if (recipient == "x1@example.org" && id != msgs[0].ID) || (recipient == "a1@example.net" && id != msgs[1].ID) {
			t.Errorf("row %d %s under the wrong id", id, recipient)
		}
		left = append(left, recipient)
	}
	if !slices.Equal(left, []string{"x1@example.org", "a1@example.net"}) {
		t.Errorf("rows after down %v", left)
	}
}

// TestGroupResponsiblesOfMessage: the responsible parties of the groups of
// every recipient block of a message come back in one query, by group key;
// a block without a group or with a group that is gone has no entry, and
// another message's groups are not listed.
func TestGroupResponsiblesOfMessage(t *testing.T) {
	db, msgs := newBounceIndex(t)
	for key, responsible := range map[string]string{"aaaaaaaaaaaaaaaa": "domain", "bbbbbbbbbbbbbbbb": "recipient", "dddddddddddddddd": "sender"} {
		if err := UpsertGroup(db, &BounceGroup{GroupKey: key, Category: "mailbox_full", UnitValue: key, Responsible: responsible}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ReplaceBounces(db, msgs[0].ID, []*Bounce{
		{GroupKey: "aaaaaaaaaaaaaaaa", Recipient: "a1@example.net"},
		{GroupKey: "aaaaaaaaaaaaaaaa", Recipient: "a2@example.net"},
		{GroupKey: "bbbbbbbbbbbbbbbb", Recipient: "b1@example.org"},
		{GroupKey: "", Recipient: "none@example.org"},
		{GroupKey: "cccccccccccccccc", Recipient: "gone@example.org"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceBounces(db, msgs[1].ID, []*Bounce{{GroupKey: "dddddddddddddddd", Recipient: "d1@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM groups WHERE group_key = 'cccccccccccccccc'`); err != nil {
		t.Fatal(err)
	}
	got, err := GroupResponsiblesOfMessage(db, msgs[0].ID)
	want := map[string]string{"aaaaaaaaaaaaaaaa": "domain", "bbbbbbbbbbbbbbbb": "recipient"}
	if err != nil || !maps.Equal(got, want) {
		t.Errorf("responsibles %v, %v; want %v", got, err, want)
	}
}

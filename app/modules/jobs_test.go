package modules

import (
	"context"
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

// testSamples are the raw messages seeded into a mailbox directory: two
// bounces and one ordinary mail.
var testSamples = []string{"postfix_dsn.eml", "exim_bounce.eml", "normal.eml"}

// seedMailbox registers a mailbox and copies the sample .eml files into its
// raw directory under the current data directory. It returns the mailbox and
// the mails root.
func seedMailbox(t *testing.T, db *sql.DB, key []byte, address string, samples []string) (*models.Mailbox, string) {
	t.Helper()
	mb, err := CreateMailbox(db, key, &MailboxInput{Address: address, ImapHost: "imap.example.test",
		ImapUsername: "u", ImapPassword: "p"})
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	mailsRoot, err := MailsDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := mailengine.MailboxDir(mailsRoot, mb.Address)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i, name := range samples {
		raw, err := os.ReadFile(filepath.Join("mailengine", "testdata", name))
		if err != nil {
			t.Fatalf("read sample %s: %v", name, err)
		}
		msgKey := mailengine.MessageKey(time.Date(2025, 9, 1+i, 0, 0, 0, 0, time.UTC), "INBOX", 1, uint32(i+1))
		if !mailengine.ValidMessageKey(msgKey) {
			t.Fatalf("invalid message key %s", msgKey)
		}
		if err := os.WriteFile(filepath.Join(dir, msgKey+".eml"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return mb, mailsRoot
}

func newTestJobManager(t *testing.T, db *sql.DB) (*JobManager, []byte) {
	t.Helper()
	key, err := LoadSecretKey()
	if err != nil {
		t.Fatal(err)
	}
	mailsRoot, err := MailsDir()
	if err != nil {
		t.Fatal(err)
	}
	agentRoot, err := AgentDir()
	if err != nil {
		t.Fatal(err)
	}
	return NewJobManager(db, key, mailsRoot, agentRoot, nil), key
}

func TestEnqueueJobValidation(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	if _, _, err := jm.Enqueue("bogus", 0, "", "t"); err == nil || !strings.Contains(err.Error(), "unknown job kind") {
		t.Errorf("unknown kind: %v", err)
	}
	if _, _, err := jm.Enqueue(JobKindAnalyze, 0, "", "t"); err == nil || !strings.Contains(err.Error(), "requires a mailbox") {
		t.Errorf("analyze without mailbox: %v", err)
	}
	if _, _, err := jm.Enqueue(JobKindCheck, 999, "", "t"); err == nil || !strings.Contains(err.Error(), "mailbox not found") {
		t.Errorf("unknown mailbox: %v", err)
	}
	mb, err := CreateMailbox(db, key, &MailboxInput{Address: "a@example.test", ImapHost: "h", ImapUsername: "u", ImapPassword: "p"})
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := jm.Enqueue(JobKindCheck, mb.ID, "ignored-target", "web:alice")
	if err != nil || !created {
		t.Fatalf("first enqueue: created=%v err=%v", created, err)
	}
	if first.Target != "" {
		t.Errorf("target not cleared for a check job: %q", first.Target)
	}
	if first.Status != JobStatusQueued || first.RequestedBy != "web:alice" || first.MailboxID.Int64 != mb.ID {
		t.Errorf("unexpected job %+v", first)
	}
	dup, created, err := jm.Enqueue(JobKindCheck, mb.ID, "", "cli")
	if err != nil || created || dup.ID != first.ID {
		t.Errorf("duplicate: created=%v id=%d (want %d) err=%v", created, dup.ID, first.ID, err)
	}
	// A different target of an analyze job is a different job; the same one is suppressed.
	a1, created, err := jm.Enqueue(JobKindAnalyze, mb.ID, "abc", "cli")
	if err != nil || !created {
		t.Fatalf("analyze abc: %v %v", created, err)
	}
	if _, created, _ := jm.Enqueue(JobKindAnalyze, mb.ID, "def", "cli"); !created {
		t.Errorf("analyze def should be a new job")
	}
	if again, created, _ := jm.Enqueue(JobKindAnalyze, mb.ID, "abc", "cli"); created || again.ID != a1.ID {
		t.Errorf("analyze abc duplicated")
	}
	// An all-mailbox check and a per-mailbox check are distinct.
	if _, created, err := jm.Enqueue(JobKindCheck, 0, "", "scheduler"); err != nil || !created {
		t.Errorf("all-mailbox check: %v %v", created, err)
	}
	// Once the job finished an identical one can be queued again.
	if err := models.FinishJob(db, first.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, created, err := jm.Enqueue(JobKindCheck, mb.ID, "", "cli"); err != nil || !created {
		t.Errorf("re-enqueue after finish: %v %v", created, err)
	}
}

// closedPort returns a loopback port that nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestRunJobCheckRecordsConnectionError(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	mb, err := CreateMailbox(db, key, &MailboxInput{Address: "down@example.test", ImapHost: "127.0.0.1",
		ImapPort: closedPort(t), ImapSecurity: IMAPSecurityNone, ImapUsername: "u", ImapPassword: "p"})
	if err != nil {
		t.Fatal(err)
	}
	job := &models.Job{Kind: JobKindCheck, MailboxID: sql.NullInt64{Int64: mb.ID, Valid: true}}
	var lines []string
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := jm.RunJob(ctx, job, func(m string) { lines = append(lines, m) })
	if err == nil {
		t.Fatalf("expected an error, got result %q", result)
	}
	if !strings.Contains(err.Error(), "down@example.test") {
		t.Errorf("error does not name the mailbox: %v", err)
	}
	fresh, err := models.GetMailboxByID(db, mb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.LastCheckStatus != "error" || fresh.LastCheckError == "" || !fresh.LastCheckedAt.Valid {
		t.Errorf("mailbox row not updated: status %q error %q checked %v", fresh.LastCheckStatus, fresh.LastCheckError, fresh.LastCheckedAt.Valid)
	}
	if joined := strings.Join(lines, "\n"); !strings.Contains(joined, "[down@example.test] error:") {
		t.Errorf("progress lacks the error line:\n%s", joined)
	}
	// A disabled mailbox is skipped by an all-mailbox check.
	if err := models.UpdateMailbox(db, &models.Mailbox{ID: mb.ID, Address: mb.Address, ImapHost: mb.ImapHost,
		ImapPort: mb.ImapPort, ImapSecurity: mb.ImapSecurity, ImapUsername: mb.ImapUsername, Folder: mb.Folder,
		Enabled: false, InitialDays: mb.InitialDays, RecentDays: mb.RecentDays}); err != nil {
		t.Fatal(err)
	}
	if result, err := jm.RunJob(ctx, &models.Job{Kind: JobKindCheck}, nil); err != nil || result != "no enabled mailbox" {
		t.Errorf("all-mailbox check with only a disabled mailbox: %q, %v", result, err)
	}
}

func TestRunJobReindexAndAnalyze(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	mb, mailsRoot := seedMailbox(t, db, key, "ops@example.test", testSamples)
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	job := &models.Job{Kind: JobKindReindex, MailboxID: sql.NullInt64{Int64: mb.ID, Valid: true}, RequestedBy: "cli"}
	var lines []string
	result, err := jm.RunJob(context.Background(), job, func(m string) { lines = append(lines, m) })
	if err != nil {
		t.Fatalf("reindex: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if !strings.Contains(result, "ops@example.test: messages 3, bounces 2, groups 2") {
		t.Errorf("result = %q", result)
	}
	if active, _ := models.ListActiveJobs(db); len(active) != 0 {
		t.Errorf("analyze queued although the agent is disabled: %+v", active[0])
	}
	idx, err := mailengine.OpenIndex(context.Background(), mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := models.ListGroups(idx, models.GroupFilter{})
	idx.Close()
	if err != nil || len(groups) != 2 {
		t.Fatalf("groups: %d, %v", len(groups), err)
	}

	// Reclassify over every mailbox (mailbox_id NULL) works the same way.
	result, err = jm.RunJob(context.Background(), &models.Job{Kind: JobKindReclassify}, nil)
	if err != nil || !strings.Contains(result, "messages 3, bounces 2, groups 2") {
		t.Errorf("reclassify: %q, %v", result, err)
	}

	// Analyze with an unregistered provider fails clearly, even for an
	// explicit request, and touches no report.
	if err := models.SetSetting(db, SettingAgentProvider, "no-such-provider"); err != nil {
		t.Fatal(err)
	}
	analyze := &models.Job{Kind: JobKindAnalyze, MailboxID: sql.NullInt64{Int64: mb.ID, Valid: true}, Target: analyzeAllTarget}
	if _, err := jm.RunJob(context.Background(), analyze, nil); err == nil || !strings.Contains(err.Error(), `agent provider "no-such-provider" is not registered`) {
		t.Errorf("analyze with an unknown provider: %v", err)
	}
	if _, err := jm.RunJob(context.Background(), &models.Job{Kind: JobKindAnalyze}, nil); err == nil || !strings.Contains(err.Error(), "requires a mailbox") {
		t.Errorf("analyze without mailbox: %v", err)
	}
	// With the agent enabled but the provider unknown, a rebuild queues no
	// analysis and says why.
	if err := SetAgentEnabled(db, true); err != nil {
		t.Fatal(err)
	}
	lines = nil
	if _, err := jm.RunJob(context.Background(), job, func(m string) { lines = append(lines, m) }); err != nil {
		t.Fatal(err)
	}
	if active, _ := models.ListActiveJobs(db); len(active) != 0 {
		t.Errorf("analyze queued with an unavailable provider")
	}
	if !strings.Contains(strings.Join(lines, "\n"), "not available; analysis skipped") {
		t.Errorf("no skip notice:\n%s", strings.Join(lines, "\n"))
	}
	// Unknown kinds are rejected by the dispatcher.
	if _, err := jm.RunJob(context.Background(), &models.Job{Kind: "bogus"}, nil); err == nil {
		t.Errorf("unknown kind accepted")
	}
}

func TestRunJobInlineRunsFollowUps(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	mb, _ := seedMailbox(t, db, key, "ops@example.test", testSamples)
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	// While the first job runs, a follow-up job by the same requester is
	// queued (as the check / rebuild jobs do for analysis); the inline runner
	// must execute it before returning.
	var once sync.Once
	var lines []string
	echo := func(m string) {
		lines = append(lines, m)
		once.Do(func() {
			if _, _, err := jm.Enqueue(JobKindReclassify, mb.ID, "", RequestedByCLI); err != nil {
				t.Errorf("queue follow-up: %v", err)
			}
			// A job of another requester must be left alone.
			if _, _, err := jm.Enqueue(JobKindCheck, 0, "", "web:someone"); err != nil {
				t.Errorf("queue foreign job: %v", err)
			}
		})
	}
	job, err := jm.RunJobInline(context.Background(), JobKindReindex, mb.ID, "", RequestedByCLI, echo)
	if err != nil {
		t.Fatalf("inline: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if job.Status != JobStatusDone || !strings.Contains(job.Result, "messages 3") || job.Progress == "" {
		t.Errorf("first job %+v", job)
	}
	all, err := models.ListJobs(db, 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("jobs: %d, %v", len(all), err)
	}
	byKind := map[string]*models.Job{}
	for _, j := range all {
		byKind[j.Kind] = j
	}
	if byKind[JobKindReclassify].Status != JobStatusDone {
		t.Errorf("follow-up not run: %+v", byKind[JobKindReclassify])
	}
	if byKind[JobKindCheck].Status != JobStatusQueued {
		t.Errorf("foreign job touched: %+v", byKind[JobKindCheck])
	}
	if !strings.Contains(strings.Join(lines, "\n"), "--- job #") {
		t.Errorf("follow-up banner missing:\n%s", strings.Join(lines, "\n"))
	}
	// A second identical inline run while the foreign check job is queued
	// is fine, but an identical queued job is reported instead of run twice.
	if _, err := jm.RunJobInline(context.Background(), JobKindCheck, 0, "", RequestedByCLI, nil); err == nil || !strings.Contains(err.Error(), "already queued") {
		t.Errorf("duplicate inline: %v", err)
	}
}

func TestResetStaleJobs(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	mb, mailsRoot := seedMailbox(t, db, key, "ops@example.test", testSamples[:1])
	if _, _, err := jm.Enqueue(JobKindCheck, mb.ID, "", "cli"); err != nil {
		t.Fatal(err)
	}
	running, err := models.ClaimNextJob(db)
	if err != nil || running == nil {
		t.Fatalf("claim: %v", err)
	}
	old, _, err := jm.Enqueue(JobKindReindex, mb.ID, "", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if err := models.FinishJob(db, old.ID, "done", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE jobs SET finished_at = ? WHERE id = ?`, time.Now().Add(-40*24*time.Hour).UTC(), old.ID); err != nil {
		t.Fatal(err)
	}
	// A running agent report in the index is reset too.
	idx, err := mailengine.OpenIndex(context.Background(), mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := models.UpsertGroup(idx, &models.BounceGroup{GroupKey: "0123456789abcdef", Title: "g"}); err != nil {
		t.Fatal(err)
	}
	rep := &models.AgentReport{GroupKey: "0123456789abcdef", Provider: "codex"}
	if err := models.InsertAgentReport(idx, rep); err != nil {
		t.Fatal(err)
	}
	idx.Close()

	ResetStaleJobs(db, mailsRoot)

	j, err := models.GetJobByID(db, running.ID)
	if err != nil || j.Status != JobStatusError || !strings.Contains(j.ErrorMessage, "restart") || !j.FinishedAt.Valid {
		t.Errorf("running job not reset: %+v, %v", j, err)
	}
	if _, err := models.GetJobByID(db, old.ID); err != sql.ErrNoRows {
		t.Errorf("old finished job not deleted: %v", err)
	}
	idx, err = mailengine.OpenIndex(context.Background(), mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	latest, err := models.LatestAgentReport(idx, "0123456789abcdef")
	if err != nil || latest.Status != "error" {
		t.Errorf("running report not reset: %+v, %v", latest, err)
	}
}

func TestNewProgressLines(t *testing.T) {
	if got := newProgressLines("", "a\nb"); len(got) != 2 {
		t.Errorf("first read: %v", got)
	}
	if got := newProgressLines("a\nb", "a\nb\nc\nd"); len(got) != 2 || got[0] != "c" {
		t.Errorf("appended: %v", got)
	}
	if got := newProgressLines("a\nb", "b\nc"); len(got) != 1 || got[0] != "c" {
		t.Errorf("scrolled: %v", got)
	}
	if got := newProgressLines("a\nb", "a\nb"); len(got) != 0 {
		t.Errorf("unchanged: %v", got)
	}
	if got := newProgressLines("x", ""); got != nil {
		t.Errorf("empty: %v", got)
	}
}

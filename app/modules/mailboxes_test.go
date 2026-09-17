package modules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

// TestMailboxRenameAndDeleteMoveEveryDirectory checks that renaming a mailbox
// moves its raw files directory, its index and its agent workspaces, and that
// deleting it removes all three.
func TestMailboxRenameAndDeleteMoveEveryDirectory(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	mb, mailsRoot := seedMailbox(t, db, key, "old@example.test", testSamples[:1])
	agentRoot, err := AgentDir()
	if err != nil {
		t.Fatal(err)
	}
	oldIndex := mailengine.MailboxIndexPath(mailsRoot, mb.Address)
	if err := os.WriteFile(oldIndex, []byte("index"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldRun := filepath.Join(AgentWorkspaceDir(agentRoot, mb.Address), "0123456789abcdef", "1")
	if err := os.MkdirAll(oldRun, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldRun, "RESULT.log"), []byte("Result: Success\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	in := MailboxInputFrom(mb)
	in.Address = "new@example.test"
	if err := UpdateMailbox(db, key, mailsRoot, agentRoot, mb, in); err != nil {
		t.Fatalf("rename: %v", err)
	}
	newRun := filepath.Join(AgentWorkspaceDir(agentRoot, "new@example.test"), "0123456789abcdef", "1", "RESULT.log")
	for _, p := range []string{
		mailengine.MailboxDir(mailsRoot, "new@example.test"),
		mailengine.MailboxIndexPath(mailsRoot, "new@example.test"),
		newRun,
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s not moved: %v", p, err)
		}
	}
	for _, p := range []string{mailengine.MailboxDir(mailsRoot, "old@example.test"), oldIndex, AgentWorkspaceDir(agentRoot, "old@example.test")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still there after the rename: %v", p, err)
		}
	}
	stored, err := models.GetMailboxByID(db, mb.ID)
	if err != nil || stored.Address != "new@example.test" {
		t.Fatalf("stored mailbox: %+v, %v", stored, err)
	}
	// A rename onto an address whose data exists is refused before anything
	// is moved.
	if err := os.MkdirAll(AgentWorkspaceDir(agentRoot, "taken@example.test"), 0o700); err != nil {
		t.Fatal(err)
	}
	in.Address = "taken@example.test"
	if err := UpdateMailbox(db, key, mailsRoot, agentRoot, stored, in); err == nil {
		t.Error("rename onto existing agent workspaces must fail")
	}
	if stored, err := models.GetMailboxByID(db, mb.ID); err != nil || stored.Address != "new@example.test" {
		t.Errorf("failed rename changed the row: %+v %v", stored, err)
	}
	for _, p := range []string{mailengine.MailboxDir(mailsRoot, "new@example.test"), mailengine.MailboxIndexPath(mailsRoot, "new@example.test"), newRun} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("a refused rename moved %s: %v", p, err)
		}
	}

	// --keep-data keeps every directory; a plain delete removes them all.
	stored, _ = models.GetMailboxByID(db, mb.ID)
	if err := DeleteMailbox(db, mailsRoot, agentRoot, stored, true); err != nil {
		t.Fatalf("delete keeping data: %v", err)
	}
	if _, err := os.Stat(newRun); err != nil {
		t.Errorf("keep_data removed the agent workspaces: %v", err)
	}
	if _, err := os.Stat(mailengine.MailboxDir(mailsRoot, "new@example.test")); err != nil {
		t.Errorf("keep_data removed the mail directory: %v", err)
	}
	if err := DeleteMailbox(db, mailsRoot, agentRoot, stored, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, p := range []string{
		mailengine.MailboxDir(mailsRoot, "new@example.test"),
		mailengine.MailboxIndexPath(mailsRoot, "new@example.test"),
		AgentWorkspaceDir(agentRoot, "new@example.test"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists after delete: %v", p, err)
		}
	}
	if _, err := models.GetMailboxByID(db, mb.ID); err == nil {
		t.Error("mailbox row still exists")
	}
	// Nothing but the mails and agent directories and the master database
	// is left in the data directory.
	dataDir, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch e.Name() {
		case DBFileName, DBFileName + "-wal", DBFileName + "-shm", MailsDirName, AgentDirName:
		default:
			t.Errorf("unexpected entry in the data directory: %s", e.Name())
		}
	}
}

// queuedJob queues a job of the kind for the mailbox and returns it.
func queuedJob(t *testing.T, db *sql.DB, kind string, mb *models.Mailbox) *models.Job {
	t.Helper()
	job, created, err := EnqueueJob(db, kind, mb.ID, "", "cli", 0, nil)
	if err != nil || !created {
		t.Fatalf("enqueue %s: created=%v err=%v", kind, created, err)
	}
	return job
}

// TestDeleteMailboxCancelsQueuedJobs checks that deleting a mailbox cancels
// its waiting jobs in the same step as the row, leaves the jobs of other
// mailboxes alone, and that a running job makes the deletion (and a rename)
// fail with ErrMailboxBusy without touching anything.
func TestDeleteMailboxCancelsQueuedJobs(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	mb, mailsRoot := seedMailbox(t, db, key, "busy@example.test", testSamples[:1])
	other, _ := seedMailbox(t, db, key, "other@example.test", nil)
	agentRoot, err := AgentDir()
	if err != nil {
		t.Fatal(err)
	}
	waiting := queuedJob(t, db, JobKindSync, mb)
	waitingOther := queuedJob(t, db, JobKindSync, other)
	running := queuedJob(t, db, JobKindGroup, mb)
	if claimed, err := models.ClaimJobByID(db, running.ID); err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}

	// A running job blocks the deletion and the rename; nothing changes.
	if err := DeleteMailbox(db, mailsRoot, agentRoot, mb, false); !errors.Is(err, ErrMailboxBusy) {
		t.Fatalf("delete while running: %v, want ErrMailboxBusy", err)
	}
	if _, err := models.GetMailboxByID(db, mb.ID); err != nil {
		t.Fatalf("row gone after a refused delete: %v", err)
	}
	if j, _ := models.GetJobByID(db, waiting.ID); j == nil || j.Status != JobStatusQueued {
		t.Errorf("a refused delete changed the queued job: %+v", j)
	}
	if _, err := os.Stat(mailengine.MailboxDir(mailsRoot, mb.Address)); err != nil {
		t.Errorf("a refused delete removed the mail directory: %v", err)
	}
	in := MailboxInputFrom(mb)
	in.Address = "renamed@example.test"
	if err := UpdateMailbox(db, key, mailsRoot, agentRoot, mb, in); !errors.Is(err, ErrMailboxBusy) {
		t.Fatalf("rename while running: %v, want ErrMailboxBusy", err)
	}
	if mb.Address != "busy@example.test" {
		t.Errorf("a refused rename changed the address in memory: %s", mb.Address)
	}
	if _, err := os.Stat(mailengine.MailboxDir(mailsRoot, "renamed@example.test")); !os.IsNotExist(err) {
		t.Errorf("a refused rename moved the mail directory: %v", err)
	}
	// Other fields can still be changed while a job runs.
	in = MailboxInputFrom(mb)
	in.DisplayName = "Busy"
	if err := UpdateMailbox(db, key, mailsRoot, agentRoot, mb, in); err != nil {
		t.Fatalf("update without rename while running: %v", err)
	}

	// Once the job finished the deletion cancels the waiting job and
	// removes the row; the other mailbox's job stays queued.
	if err := models.FinishJob(db, running.ID, "done", ""); err != nil {
		t.Fatal(err)
	}
	if err := DeleteMailbox(db, mailsRoot, agentRoot, mb, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := models.GetMailboxByID(db, mb.ID); err != sql.ErrNoRows {
		t.Errorf("row still there: %v", err)
	}
	j, err := models.GetJobByID(db, waiting.ID)
	if err != nil || j.Status != JobStatusCanceled || !j.FinishedAt.Valid || j.MailboxID.Valid {
		t.Errorf("waiting job after the delete = %+v (err %v), want canceled with finished_at and no mailbox", j, err)
	}
	if j, _ := models.GetJobByID(db, waitingOther.ID); j == nil || j.Status != JobStatusQueued || !j.MailboxID.Valid {
		t.Errorf("the other mailbox's job was touched: %+v", j)
	}
	if j, _ := models.GetJobByID(db, running.ID); j == nil || j.Status != JobStatusDone {
		t.Errorf("the finished job was touched: %+v", j)
	}
}

// TestRenameMovesFilesBackWhenRowUpdateFails checks that a rename whose
// row update fails puts the mail data back under the old address.
func TestRenameMovesFilesBackWhenRowUpdateFails(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	mb, mailsRoot := seedMailbox(t, db, key, "old@example.test", testSamples[:1])
	agentRoot, err := AgentDir()
	if err != nil {
		t.Fatal(err)
	}
	oldDir, newDir := mailengine.MailboxDir(mailsRoot, mb.Address), mailengine.MailboxDir(mailsRoot, "new@example.test")
	// Every statement goes through one read-only connection: the file moves
	// succeed, the row update cannot.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA query_only = 1`); err != nil {
		t.Fatal(err)
	}
	in := MailboxInputFrom(mb)
	in.Address = "new@example.test"
	err = UpdateMailbox(db, key, mailsRoot, agentRoot, mb, in)
	if _, perr := db.Exec(`PRAGMA query_only = 0`); perr != nil {
		t.Fatal(perr)
	}
	if err == nil {
		t.Fatal("rename succeeded on a read-only connection")
	}
	if mb.Address != "old@example.test" {
		t.Errorf("address in memory after the failed rename: %s", mb.Address)
	}
	if _, err := os.Stat(oldDir); err != nil {
		t.Errorf("mail directory not moved back: %v", err)
	}
	if _, err := os.Stat(newDir); !os.IsNotExist(err) {
		t.Errorf("mail directory left under the new name: %v", err)
	}
	if stored, err := models.GetMailboxByID(db, mb.ID); err != nil || stored.Address != "old@example.test" {
		t.Errorf("stored row after the failed rename: %+v %v", stored, err)
	}
}

// TestValidateMailboxInputHosts checks the accepted host forms: names and
// IP literals (IPv6 included), never "host:port".
func TestValidateMailboxInputHosts(t *testing.T) {
	for host, ok := range map[string]bool{
		"imap.example.test": true,
		"localhost":         true,
		"192.0.2.10":        true,
		"::1":               true,
		"2001:db8::25":      true,
		"imap.example:993":  false,
		"[::1]":             false,
		"[::1]:993":         false,
		"imap example":      false,
		"imap/example":      false,
	} {
		in := &MailboxInput{Address: "a@example.test", ImapHost: host, ImapUsername: "u"}
		err := ValidateMailboxInput(in)
		if (err == nil) != ok {
			t.Errorf("host %q: err %v, want ok=%v", host, err, ok)
		}
	}
}

// TestConnectionTestUsesStoredPasswordOnlyForSameConnection checks that the
// stored password is used only when host, port, security and username are
// the stored ones; otherwise the password is required.
func TestConnectionTestUsesStoredPasswordOnlyForSameConnection(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	port := closedPort(t)
	mb := unreachableMailbox(t, db, key, "probe@example.test", "127.0.0.1", port)
	same := MailboxInputFrom(mb)
	err = TestMailboxConnection(context.Background(), db, key, same)
	if err == nil || strings.Contains(err.Error(), "imap_password") {
		t.Errorf("same connection without password: %v, want a connection failure", err)
	}
	for name, change := range map[string]func(in *MailboxInput){
		"host":     func(in *MailboxInput) { in.ImapHost = "127.0.0.2" },
		"port":     func(in *MailboxInput) { in.ImapPort = port + 1 },
		"security": func(in *MailboxInput) { in.ImapSecurity = IMAPSecurityStartTLS },
		"username": func(in *MailboxInput) { in.ImapUsername = "someone-else" },
	} {
		in := MailboxInputFrom(mb)
		change(in)
		err := TestMailboxConnection(context.Background(), db, key, in)
		if err == nil || !strings.Contains(err.Error(), "imap_password is required") {
			t.Errorf("changed %s without password: %v, want the password to be required", name, err)
		}
	}
	// With a password of its own the changed connection is tried.
	in := MailboxInputFrom(mb)
	in.ImapUsername = "someone-else"
	in.ImapPassword = "secret"
	if err := TestMailboxConnection(context.Background(), db, key, in); err == nil || strings.Contains(err.Error(), "imap_password") {
		t.Errorf("changed username with password: %v, want a connection failure", err)
	}
}

// TestUpdateMailboxRequiresPasswordWhenConnectionChanges checks that an
// update without a password is accepted only while the connection settings
// stay the same: changing the host, port, security or username needs a new
// password (nothing is saved otherwise), while the other fields keep the
// stored password.
func TestUpdateMailboxRequiresPasswordWhenConnectionChanges(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	mb, mailsRoot := seedMailbox(t, db, key, "conn@example.test", nil)
	agentRoot, err := AgentDir()
	if err != nil {
		t.Fatal(err)
	}
	storedEnc := mb.ImapPasswordEnc
	for name, change := range map[string]func(in *MailboxInput){
		"host":     func(in *MailboxInput) { in.ImapHost = "imap2.example.test" },
		"port":     func(in *MailboxInput) { in.ImapPort = 143 },
		"security": func(in *MailboxInput) { in.ImapSecurity = IMAPSecurityStartTLS },
		"username": func(in *MailboxInput) { in.ImapUsername = "someone-else" },
	} {
		in := MailboxInputFrom(mb)
		change(in)
		if err := UpdateMailbox(db, key, mailsRoot, agentRoot, mb, in); !errors.Is(err, ErrPasswordRequired) {
			t.Errorf("changed %s without password: %v, want ErrPasswordRequired", name, err)
		}
		stored, err := models.GetMailboxByID(db, mb.ID)
		if err != nil || stored.ImapHost != "imap.example.test" || stored.ImapPort != DefaultIMAPPort ||
			stored.ImapSecurity != IMAPSecuritySSL || stored.ImapUsername != "u" || stored.ImapPasswordEnc != storedEnc {
			t.Errorf("changed %s without password: the row changed: %+v (err %v)", name, stored, err)
		}
	}
	// The host may change its letter case without a password.
	in := MailboxInputFrom(mb)
	in.ImapHost = "IMAP.example.test"
	if err := UpdateMailbox(db, key, mailsRoot, agentRoot, mb, in); err != nil {
		t.Errorf("host case change without password: %v", err)
	}
	// Other fields are fine without a password and keep the stored one.
	in = MailboxInputFrom(mb)
	in.DisplayName = "Renamed"
	in.Folder = "Bounces"
	in.RecentDays = 7
	if err := UpdateMailbox(db, key, mailsRoot, agentRoot, mb, in); err != nil {
		t.Fatalf("update of other fields without password: %v", err)
	}
	stored, err := models.GetMailboxByID(db, mb.ID)
	if err != nil || stored.DisplayName != "Renamed" || stored.Folder != "Bounces" || stored.RecentDays != 7 {
		t.Errorf("other fields not saved: %+v (err %v)", stored, err)
	}
	if p, err := MailboxPassword(key, stored); err != nil || p != "p" {
		t.Errorf("stored password after the update = %q (err %v), want the original", p, err)
	}
	// With a password the connection change is saved, password included.
	in = MailboxInputFrom(mb)
	in.ImapHost = "imap2.example.test"
	in.ImapUsername = "someone-else"
	in.ImapPassword = "new-secret"
	if err := UpdateMailbox(db, key, mailsRoot, agentRoot, mb, in); err != nil {
		t.Fatalf("connection change with password: %v", err)
	}
	stored, err = models.GetMailboxByID(db, mb.ID)
	if err != nil || stored.ImapHost != "imap2.example.test" || stored.ImapUsername != "someone-else" {
		t.Errorf("connection change not saved: %+v (err %v)", stored, err)
	}
	if p, err := MailboxPassword(key, stored); err != nil || p != "new-secret" {
		t.Errorf("password after the connection change = %q (err %v)", p, err)
	}

	// The CLI: the same rule, with a hint about the flags.
	var exitErr *ExitError
	err = (&MailboxUpdateCmd{Address: mb.Address, Host: "imap3.example.test"}).Run()
	if !errors.As(err, &exitErr) || exitErr.Code != ExitArgument || !strings.Contains(err.Error(), "--password") {
		t.Errorf("mailbox update --host without password: %v, want an argument error naming --password", err)
	}
	captureStdout(t, func() {
		if err := (&MailboxUpdateCmd{Address: mb.Address, Host: "imap3.example.test", Password: "third"}).Run(); err != nil {
			t.Errorf("mailbox update --host --password: %v", err)
		}
		if err := (&MailboxUpdateCmd{Address: mb.Address, DisplayName: "Ops"}).Run(); err != nil {
			t.Errorf("mailbox update --display-name without password: %v", err)
		}
	})
	stored, err = models.GetMailboxByID(db, mb.ID)
	if err != nil || stored.ImapHost != "imap3.example.test" || stored.DisplayName != "Ops" {
		t.Errorf("row after the CLI updates: %+v (err %v)", stored, err)
	}
	if p, err := MailboxPassword(key, stored); err != nil || p != "third" {
		t.Errorf("password after the CLI update = %q (err %v)", p, err)
	}
}

// TestMailboxAddressConflictsOnSanitizedName checks that an address whose
// sanitized form (the data directory name) is the sanitized form of another
// mailbox's address is refused on registration and on rename, in both
// directions, while a rename that keeps the mailbox's own name is allowed.
func TestMailboxAddressConflictsOnSanitizedName(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
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
	newInput := func(address string) *MailboxInput {
		return &MailboxInput{Address: address, ImapHost: "imap.example.test", ImapUsername: "u", ImapPassword: "p"}
	}
	if mailengine.SanitizeAddress("a!b@example.test") != mailengine.SanitizeAddress("a_b@example.test") {
		t.Fatal("the test addresses do not share a sanitized name")
	}
	conflict := func(err error, address, other string) bool {
		return err != nil && err.Error() == fmt.Sprintf("mailbox %q conflicts with %q (same data directory name)", address, other)
	}

	// Registration: the sanitized name of a!b@ is taken by a_b@ ...
	first, err := CreateMailbox(db, key, newInput("a_b@example.test"))
	if err != nil {
		t.Fatalf("create a_b@: %v", err)
	}
	if _, err := CreateMailbox(db, key, newInput("a!b@example.test")); !conflict(err, "a!b@example.test", "a_b@example.test") {
		t.Errorf("create a!b@ next to a_b@: %v, want the conflict error", err)
	}
	// ... and the other way round.
	if err := DeleteMailbox(db, mailsRoot, agentRoot, first, false); err != nil {
		t.Fatal(err)
	}
	second, err := CreateMailbox(db, key, newInput("a!b@example.test"))
	if err != nil {
		t.Fatalf("create a!b@: %v", err)
	}
	if _, err := CreateMailbox(db, key, newInput("a_b@example.test")); !conflict(err, "a_b@example.test", "a!b@example.test") {
		t.Errorf("create a_b@ next to a!b@: %v, want the conflict error", err)
	}
	// An identical address is still reported as existing.
	if _, err := CreateMailbox(db, key, newInput("a!b@example.test")); err == nil || err.Error() != `mailbox "a!b@example.test" already exists` {
		t.Errorf("create a!b@ twice: %v, want the already-exists error", err)
	}

	// Rename: the mailbox's own sanitized name does not conflict with itself
	// (a!b@ -> a?b@ keeps the name a_b@), another mailbox's name does.
	in := MailboxInputFrom(second)
	in.Address = "a?b@example.test"
	if err := UpdateMailbox(db, key, mailsRoot, agentRoot, second, in); err != nil {
		t.Fatalf("rename to the same sanitized name: %v", err)
	}
	if second.Address != "a?b@example.test" {
		t.Errorf("address after the rename = %q", second.Address)
	}
	third, err := CreateMailbox(db, key, newInput("x_y@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	in = MailboxInputFrom(third)
	in.Address = "a#b@example.test"
	if err := UpdateMailbox(db, key, mailsRoot, agentRoot, third, in); !conflict(err, "a#b@example.test", "a?b@example.test") {
		t.Errorf("rename x_y@ to a#b@ next to a?b@: %v, want the conflict error", err)
	}
	if stored, err := models.GetMailboxByID(db, third.ID); err != nil || stored.Address != "x_y@example.test" {
		t.Errorf("row after the refused rename: %+v (err %v)", stored, err)
	}
	in = MailboxInputFrom(second)
	in.Address = "x!y@example.test"
	if err := UpdateMailbox(db, key, mailsRoot, agentRoot, second, in); !conflict(err, "x!y@example.test", "x_y@example.test") {
		t.Errorf("rename a?b@ to x!y@ next to x_y@: %v, want the conflict error", err)
	}
	if stored, err := models.GetMailboxByID(db, second.ID); err != nil || stored.Address != "a?b@example.test" {
		t.Errorf("row after the refused rename: %+v (err %v)", stored, err)
	}
}

// TestImapEndpointBracketsIPv6 checks the "host:port" rendering of the CLI
// mailbox output: a host name or IPv4 as is, an IPv6 literal in brackets.
func TestImapEndpointBracketsIPv6(t *testing.T) {
	for host, want := range map[string]string{
		"imap.example.test": "imap.example.test:993",
		"192.0.2.10":        "192.0.2.10:993",
		"::1":               "[::1]:993",
		"2001:db8::25":      "[2001:db8::25]:993",
	} {
		got := imapEndpoint(&models.Mailbox{ImapHost: host, ImapPort: 993})
		if got != want {
			t.Errorf("imapEndpoint(%q) = %q, want %q", host, got, want)
		}
	}
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	mb, err := CreateMailbox(db, key, &MailboxInput{Address: "v6@example.test", ImapHost: "::1", ImapUsername: "u", ImapPassword: "p"})
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := (&MailboxShowCmd{Address: mb.Address}).Run(); err != nil {
			t.Errorf("mailbox show: %v", err)
		}
		if err := (&MailboxListCmd{}).Run(); err != nil {
			t.Errorf("mailbox list: %v", err)
		}
	})
	if strings.Count(out, "[::1]:993") != 2 {
		t.Errorf("IPv6 endpoint not bracketed in show and list:\n%s", out)
	}
}

package agent

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"mailcare/app/models"
)

const testGroupKey = "0123456789abcdef"

// newTestIndex creates a temporary index with one group of two bounce
// messages (with raw files on disk) and returns the index plus the roots.
func newTestIndex(t *testing.T) (db *sql.DB, mailsRoot, agentRoot, address string) {
	t.Helper()
	stubMessageFilePath(t)
	base := t.TempDir()
	mailsRoot = filepath.Join(base, "mails")
	agentRoot = filepath.Join(base, "agent")
	address = "bounce@example.com"
	db, err := models.OpenMailIndex(filepath.Join(mailsRoot, address+".sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	dir := filepath.Join(mailsRoot, address)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := models.UpsertGroup(db, &models.BounceGroup{
		GroupKey: testGroupKey, Title: "example.net - 5.1.1 - user unknown", BounceKind: "failed",
		RecipientDomain: "example.net", StatusCode: "5.1.1", SMTPCode: "550",
		DiagnosticTemplate: "550 5.1.1 <addr>: user unknown", Responsible: ResponsibleUnknown,
	}); err != nil {
		t.Fatal(err)
	}
	for i, key := range []string{"20260901-120000_aaaaaaaaaaaa", "20260902-120000_bbbbbbbbbbbb"} {
		for _, ext := range []string{"eml", "txt"} {
			if err := os.WriteFile(filepath.Join(dir, key+"."+ext), []byte("raw "+key), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		m := &models.Message{
			MessageKey: key, UID: uint32(i + 1), UIDValidity: 1, Folder: "INBOX", Subject: "Undelivered Mail",
			FromAddress: "mailer-daemon@example.com", Date: sql.NullTime{Time: time.Date(2026, 9, 1+i, 12, 0, 0, 0, time.UTC), Valid: true},
			HasText: true, IsBounce: true, BounceKind: "failed", GroupKey: testGroupKey,
		}
		if err := models.InsertMessage(db, m); err != nil {
			t.Fatal(err)
		}
		if err := models.UpsertBounce(db, &models.Bounce{
			MessageID: m.ID, OriginalRecipient: "user" + key[:1] + "@example.net", RecipientDomain: "example.net",
			Action: "failed", StatusCode: "5.1.1", SMTPCode: "550", RemoteMTA: "mx.example.net", RemoteIP: "192.0.2.10",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Counters are set directly: RefreshGroupCounters is exercised by the mail
	// engine, not by this package.
	if _, err := db.Exec(`UPDATE groups SET message_count = 2, recipient_count = 2, remote_ip_count = 1,
		first_seen = ?, last_seen = ? WHERE group_key = ?`,
		time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC), testGroupKey); err != nil {
		t.Fatal(err)
	}
	return db, mailsRoot, agentRoot, address
}

const cannedSuccess = "OpenAI Codex v0\nsession id: 1234\n" +
	ReportBegin + "\n" + sampleReport + "\n" + ReportEnd + "\n" +
	MetaBegin + "\n{\"summary\":\"宛先が存在しません。\",\"responsible\":\"recipient\",\"severity\":\"low\"}\n" + MetaEnd + "\n"

func TestAnalyzeGroupSuccess(t *testing.T) {
	db, mailsRoot, agentRoot, address := newTestIndex(t)
	registerFake(t, fakeProvider{name: "fake", command: writeFakeCLI(t, t.TempDir(), cannedSuccess)})
	templates := fstest.MapFS{
		"agent-templates/fake/AGENTS.md":       {Data: []byte("# rules\n")},
		"agent-templates/fake/sub/notes.txt":   {Data: []byte("nested\n")},
		"agent-templates/other/AGENTS.md":      {Data: []byte("not for us\n")},
		"agent-templates/fake-unrelated/x.txt": {Data: []byte("prefix collision\n")},
	}
	var lines []string
	rep, err := AnalyzeGroup(context.Background(), AnalyzeInput{
		MailsRoot: mailsRoot, AgentRoot: agentRoot, TemplatesFS: templates, Address: address, Index: db,
		GroupKey: testGroupKey, Provider: "fake",
	}, func(s string) { lines = append(lines, s) })
	if err != nil {
		t.Fatalf("AnalyzeGroup: %v", err)
	}
	if rep.Status != "completed" || rep.Provider != "fake" || rep.MessageCount != 2 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if rep.Summary != "宛先が存在しません。" || rep.Responsible != ResponsibleRecipient || rep.Severity != SeverityLow || rep.ReportMarkdown != sampleReport {
		t.Fatalf("unexpected extracted values: %+v", rep)
	}

	stored, err := models.LatestCompletedAgentReport(db, testGroupKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != rep.ID || stored.ReportMarkdown != sampleReport || !stored.FinishedAt.Valid {
		t.Fatalf("stored report differs: %+v", stored)
	}
	g, err := models.GetGroup(db, testGroupKey)
	if err != nil {
		t.Fatal(err)
	}
	if g.NeedsAnalysis {
		t.Error("needs_analysis must be cleared")
	}
	if g.Responsible != ResponsibleRecipient {
		t.Errorf("group responsible not updated from META: %q", g.Responsible)
	}

	work := filepath.Join(agentRoot, address, testGroupKey)
	for _, f := range []string{PromptFileName, ResultFileName, ReportFileName, "AGENTS.md", filepath.Join("sub", "notes.txt"), "received_prompt.txt"} {
		if _, err := os.Stat(filepath.Join(work, f)); err != nil {
			t.Errorf("workspace file %s missing: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(work, "x.txt")); err == nil {
		t.Error("templates of another provider must not be copied")
	}
	prompt, _ := os.ReadFile(filepath.Join(work, PromptFileName))
	received, _ := os.ReadFile(filepath.Join(work, "received_prompt.txt"))
	if runtime.GOOS != "windows" && string(prompt) != string(received) {
		t.Error("the CLI must receive PROMPT.md verbatim on stdin")
	}
	if !strings.Contains(string(prompt), filepath.Join(mailsRoot, address, "20260902-120000_bbbbbbbbbbbb.eml")) {
		t.Error("prompt must list the message files")
	}
	result, _ := os.ReadFile(filepath.Join(work, ResultFileName))
	if !strings.HasPrefix(string(result), "Result: Success\n") || !strings.Contains(string(result), "session id: 1234") {
		t.Errorf("RESULT.log content unexpected:\n%s", result)
	}
	report, _ := os.ReadFile(filepath.Join(work, ReportFileName))
	if strings.TrimSpace(string(report)) != sampleReport {
		t.Errorf("REPORT.md content unexpected:\n%s", report)
	}
	if len(lines) == 0 || lines[len(lines)-1] != "report stored" {
		t.Errorf("progress lines unexpected: %v", lines)
	}
}

func TestAnalyzeGroupNoReportBlock(t *testing.T) {
	db, mailsRoot, agentRoot, address := newTestIndex(t)
	registerFake(t, fakeProvider{name: "fake", command: writeFakeCLI(t, t.TempDir(), "I refuse to answer.\n")})
	rep, err := AnalyzeGroup(context.Background(), AnalyzeInput{
		MailsRoot: mailsRoot, AgentRoot: agentRoot, Address: address, Index: db, GroupKey: testGroupKey, Provider: "fake",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "error" || !strings.Contains(rep.ErrorMessage, "produced no report") {
		t.Fatalf("expected a no-report failure, got %+v", rep)
	}
	stored, err := models.LatestAgentReport(db, testGroupKey)
	if err != nil || stored.Status != "error" || stored.ErrorMessage != rep.ErrorMessage {
		t.Fatalf("failure not recorded: %+v %v", stored, err)
	}
	g, _ := models.GetGroup(db, testGroupKey)
	if !g.NeedsAnalysis {
		t.Error("needs_analysis must stay set after a failure")
	}
	result, _ := os.ReadFile(filepath.Join(agentRoot, address, testGroupKey, ResultFileName))
	if !strings.HasPrefix(string(result), "Result: Failure\nReason: ") {
		t.Errorf("RESULT.log content unexpected:\n%s", result)
	}
	if _, err := os.Stat(filepath.Join(agentRoot, address, testGroupKey, ReportFileName)); err == nil {
		t.Error("REPORT.md must not be written on failure")
	}
}

func TestAnalyzeGroupLaunchFailureAndUnknownProvider(t *testing.T) {
	db, mailsRoot, agentRoot, address := newTestIndex(t)
	registerFake(t, fakeProvider{name: "ghost", command: []string{"definitely-not-a-real-binary-mlc"}})
	rep, err := AnalyzeGroup(context.Background(), AnalyzeInput{
		MailsRoot: mailsRoot, AgentRoot: agentRoot, Address: address, Index: db, GroupKey: testGroupKey, Provider: "ghost",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "error" || !strings.Contains(rep.ErrorMessage, "could not run") {
		t.Fatalf("expected a launch failure, got %+v", rep)
	}

	rep, err = AnalyzeGroup(context.Background(), AnalyzeInput{
		MailsRoot: mailsRoot, AgentRoot: agentRoot, Address: address, Index: db, GroupKey: testGroupKey, Provider: "nope",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "error" || !strings.Contains(rep.ErrorMessage, "unknown agent provider") {
		t.Fatalf("expected an unknown-provider failure, got %+v", rep)
	}

	if _, err := AnalyzeGroup(context.Background(), AnalyzeInput{Index: db, GroupKey: "missing", Provider: "ghost"}, nil); err == nil {
		t.Fatal("a missing group cannot be recorded and must return an error")
	}
	if n, _ := models.ListAgentReports(db, "missing"); len(n) != 0 {
		t.Fatal("no report row may exist for a missing group")
	}
}

func TestAnalyzeGroupUnknownResponsibleKeepsMachineValue(t *testing.T) {
	db, mailsRoot, agentRoot, address := newTestIndex(t)
	if err := models.UpdateGroupResponsible(db, testGroupKey, ResponsibleSender); err != nil {
		t.Fatal(err)
	}
	canned := ReportBegin + "\n" + sampleReport + "\n" + ReportEnd + "\n" +
		MetaBegin + "\n{\"summary\":\"s\",\"responsible\":\"unknown\",\"severity\":\"medium\"}\n" + MetaEnd + "\n"
	registerFake(t, fakeProvider{name: "fake", command: writeFakeCLI(t, t.TempDir(), canned)})
	rep, err := AnalyzeGroup(context.Background(), AnalyzeInput{
		MailsRoot: mailsRoot, AgentRoot: agentRoot, Address: address, Index: db, GroupKey: testGroupKey, Provider: "fake",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "completed" || rep.Responsible != ResponsibleUnknown {
		t.Fatalf("the report row must keep the agent's answer: %+v", rep)
	}
	stored, err := models.LatestCompletedAgentReport(db, testGroupKey)
	if err != nil || stored.Responsible != ResponsibleUnknown {
		t.Fatalf("stored report responsible: %+v %v", stored, err)
	}
	g, err := models.GetGroup(db, testGroupKey)
	if err != nil {
		t.Fatal(err)
	}
	if g.Responsible != ResponsibleSender {
		t.Fatalf("groups.responsible must not be overwritten with unknown, got %q", g.Responsible)
	}
	if g.NeedsAnalysis {
		t.Error("needs_analysis must still be cleared on success")
	}
}

func TestAnalyzeGroupCanceled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	db, mailsRoot, agentRoot, address := newTestIndex(t)
	bin := t.TempDir()
	script := filepath.Join(bin, "slow.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	registerFake(t, fakeProvider{name: "slow", command: []string{script}})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	rep, err := AnalyzeGroup(ctx, AnalyzeInput{
		MailsRoot: mailsRoot, AgentRoot: agentRoot, Address: address, Index: db, GroupKey: testGroupKey, Provider: "slow",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "error" || !strings.Contains(rep.ErrorMessage, "canceled") {
		t.Fatalf("expected a cancellation failure, got %+v", rep)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatal("cancellation did not stop the run promptly")
	}
}

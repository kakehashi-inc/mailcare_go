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
		GroupKey: testGroupKey, Category: CategoryUserUnknown,
		UnitValue: "user2@example.net", Authority: "", Actionable: false,
		RecipientDomain: "example.net", StatusCode: "5.1.1",
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
			FromAddress: "mailer-daemon@example.com", Date: time.Date(2026, 9, 1+i, 12, 0, 0, 0, time.UTC),
			IsBounce: true, BounceKind: "failed", GroupKey: testGroupKey,
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
	// Counters (message count, recipients, IPs, first/last seen) are derived
	// from the inserted messages the same way the mail engine does it.
	if err := models.RefreshGroupCounters(db, testGroupKey); err != nil {
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
		"templates/agent/fake/AGENTS.md":       {Data: []byte("# rules\n")},
		"templates/agent/fake/sub/notes.txt":   {Data: []byte("nested\n")},
		"templates/agent/other/AGENTS.md":      {Data: []byte("not for us\n")},
		"templates/agent/fake-unrelated/x.txt": {Data: []byte("prefix collision\n")},
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
	for _, want := range []string{
		"Category: user_unknown - ", "Action unit (unit_value): user2@example.net - ",
		"Actionable by the mail administrator: no (recipient-side problem)",
	} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("prompt must carry the group's category context (%q)", want)
		}
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
	if err := models.SetGroupNeedsAnalysis(db, testGroupKey, false); err != nil {
		t.Fatal(err)
	}
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
		t.Error("needs_analysis must be set after a failure even when it was clear before the run")
	}
	result, _ := os.ReadFile(filepath.Join(agentRoot, address, testGroupKey, ResultFileName))
	if !strings.HasPrefix(string(result), "Result: Failure\nReason: ") {
		t.Errorf("RESULT.log content unexpected:\n%s", result)
	}
	if _, err := os.Stat(filepath.Join(agentRoot, address, testGroupKey, ReportFileName)); err == nil {
		t.Error("REPORT.md must not be written on failure")
	}
}

// cannedEcho is a run in which the agent copied the OUTPUT template of the
// prompt back instead of analyzing (seen with codex at a usage limit).
const cannedEcho = "OpenAI Codex v0\nsession id: 5678\n" +
	ReportBegin + "\n" + echoedTemplate + "\n" + ReportEnd + "\n" +
	MetaBegin + "\n{\"summary\":\"<one or two sentences>\",\"responsible\":\"sender|recipient|domain|unknown\",\"severity\":\"high|medium|low\"}\n" + MetaEnd + "\n"

func TestAnalyzeGroupRejectsUnusableReport(t *testing.T) {
	cases := []struct {
		name   string
		output string
		reason string
	}{
		{"echoed template", cannedEcho, "produced no report"},
		{"too short", ReportBegin + "\n## 原因の分析\n不明。\n" + ReportEnd + "\n", "too short"},
		{"one placeholder left", ReportBegin + "\n" + sampleReport + "\n2. <next action>\n" + ReportEnd + "\n", "template placeholder"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, mailsRoot, agentRoot, address := newTestIndex(t)
			// A good report first, then the unusable run.
			registerFake(t, fakeProvider{name: "fake", command: writeFakeCLI(t, t.TempDir(), cannedSuccess)})
			in := AnalyzeInput{MailsRoot: mailsRoot, AgentRoot: agentRoot, Address: address, Index: db, GroupKey: testGroupKey, Provider: "fake"}
			good, err := AnalyzeGroup(context.Background(), in, nil)
			if err != nil || good.Status != "completed" {
				t.Fatalf("first run: %+v %v", good, err)
			}
			if g, _ := models.GetGroup(db, testGroupKey); g.NeedsAnalysis {
				t.Fatal("precondition: needs_analysis must be clear after the good run")
			}
			registerFake(t, fakeProvider{name: "fake", command: writeFakeCLI(t, t.TempDir(), c.output)})
			rep, err := AnalyzeGroup(context.Background(), in, nil)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Status != "error" || !strings.Contains(rep.ErrorMessage, c.reason) {
				t.Fatalf("expected a failure mentioning %q, got %+v", c.reason, rep)
			}
			latest, err := models.LatestAgentReport(db, testGroupKey)
			if err != nil || latest.ID != rep.ID || latest.Status != "error" {
				t.Fatalf("failure not recorded as the latest report: %+v %v", latest, err)
			}
			completed, err := models.LatestCompletedAgentReport(db, testGroupKey)
			if err != nil || completed.ID != good.ID || completed.ReportMarkdown != sampleReport || completed.Summary != "宛先が存在しません。" {
				t.Fatalf("the previous good report must remain the latest completed one: %+v %v", completed, err)
			}
			g, _ := models.GetGroup(db, testGroupKey)
			if !g.NeedsAnalysis {
				t.Error("needs_analysis must be set again after an unusable report")
			}
			work := filepath.Join(agentRoot, address, testGroupKey)
			report, _ := os.ReadFile(filepath.Join(work, ReportFileName))
			if strings.TrimSpace(string(report)) != sampleReport {
				t.Errorf("REPORT.md must keep the previous good report:\n%s", report)
			}
			result, _ := os.ReadFile(filepath.Join(work, ResultFileName))
			if !strings.HasPrefix(string(result), "Result: Failure\nReason: ") || !strings.Contains(string(result), c.reason) ||
				!strings.Contains(string(result), "Response (full agent transcript):") || !strings.Contains(string(result), strings.TrimSpace(c.output)) {
				t.Errorf("RESULT.log must carry the failure reason and the raw transcript:\n%s", result)
			}
		})
	}
}

// writeEchoingFakeCLI is writeFakeCLI for a CLI that, like codex exec,
// prints the prompt it received under a "user" line before its own output.
func writeEchoingFakeCLI(t *testing.T, dir, canned string) []string {
	t.Helper()
	cannedPath := filepath.Join(dir, "canned.txt")
	if err := os.WriteFile(cannedPath, []byte(canned), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		script := filepath.Join(dir, "echo.cmd")
		body := "@echo off\r\nmore > received_prompt.txt\r\necho user\r\ntype received_prompt.txt\r\ntype \"" + cannedPath + "\"\r\n"
		if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		return []string{script}
	}
	script := filepath.Join(dir, "echo.sh")
	body := "#!/bin/sh\ncat > received_prompt.txt\necho user\ncat received_prompt.txt\ncat \"" + cannedPath + "\"\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return []string{script}
}

// analyzeGoodThenFailure runs a good report first, re-flags the group, runs
// the CLI given by argv and returns both reports.
func analyzeGoodThenFailure(t *testing.T, argv []string) (db *sql.DB, work string, good, second *models.AgentReport) {
	t.Helper()
	db, mailsRoot, agentRoot, address := newTestIndex(t)
	registerFake(t, fakeProvider{name: "fake", command: writeFakeCLI(t, t.TempDir(), cannedSuccess)})
	in := AnalyzeInput{MailsRoot: mailsRoot, AgentRoot: agentRoot, Address: address, Index: db, GroupKey: testGroupKey, Provider: "fake"}
	good, err := AnalyzeGroup(context.Background(), in, nil)
	if err != nil || good.Status != "completed" {
		t.Fatalf("first run: %+v %v", good, err)
	}
	// The good run cleared the flag; the failing run must set it again.
	if g, _ := models.GetGroup(db, testGroupKey); g.NeedsAnalysis {
		t.Fatal("precondition: needs_analysis must be clear after the good run")
	}
	registerFake(t, fakeProvider{name: "fake", command: argv})
	second, err = AnalyzeGroup(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	return db, filepath.Join(agentRoot, address, testGroupKey), good, second
}

// assertPreviousReportKept checks that a failed run left the previous good
// report as the latest completed one, REPORT.md untouched and the group still
// flagged for analysis.
func assertPreviousReportKept(t *testing.T, db *sql.DB, work string, good, failed *models.AgentReport) {
	t.Helper()
	if failed.Status != "error" {
		t.Fatalf("expected an error report, got %+v", failed)
	}
	latest, err := models.LatestAgentReport(db, testGroupKey)
	if err != nil || latest.ID != failed.ID || latest.Status != "error" || latest.ErrorMessage != failed.ErrorMessage {
		t.Fatalf("failure not recorded as the latest report: %+v %v", latest, err)
	}
	completed, err := models.LatestCompletedAgentReport(db, testGroupKey)
	if err != nil || completed.ID != good.ID || completed.ReportMarkdown != sampleReport || completed.Summary != "宛先が存在しません。" {
		t.Fatalf("the previous good report must remain the latest completed one: %+v %v", completed, err)
	}
	g, _ := models.GetGroup(db, testGroupKey)
	if !g.NeedsAnalysis {
		t.Error("needs_analysis must be set by a failure so the next sync retries the group")
	}
	report, _ := os.ReadFile(filepath.Join(work, ReportFileName))
	if strings.TrimSpace(string(report)) != sampleReport {
		t.Errorf("REPORT.md must keep the previous good report:\n%s", report)
	}
}

func TestAnalyzeGroupUsageLimitAfterPromptEcho(t *testing.T) {
	// The real codex failure: the prompt is echoed (so the transcript holds
	// the template markers), then the usage-limit error, and nothing else.
	db, work, good, rep := analyzeGoodThenFailure(t, writeEchoingFakeCLI(t, t.TempDir(), "\n"+codexUsageLimit+codexUsageLimit))
	if rep.ErrorMessage != "usage limit reached (retry after 7:22 PM)" {
		t.Fatalf("expected the usage-limit message, got %+v", rep)
	}
	assertPreviousReportKept(t, db, work, good, rep)
	result, _ := os.ReadFile(filepath.Join(work, ResultFileName))
	if !strings.HasPrefix(string(result), "Result: Failure\nReason: usage limit reached (retry after 7:22 PM)\n") ||
		!strings.Contains(string(result), "=== CONSTRAINTS ===") || !strings.Contains(string(result), strings.TrimSpace(codexUsageLimit)) {
		t.Errorf("RESULT.log must carry the reason and the full transcript including the echo:\n%s", result)
	}
}

func TestAnalyzeGroupUsageLimitWithoutEcho(t *testing.T) {
	db, work, good, rep := analyzeGoodThenFailure(t, writeFakeCLI(t, t.TempDir(), "Error: HTTP 429 Too Many Requests\n"))
	if rep.ErrorMessage != "usage limit reached" {
		t.Fatalf("expected the generic usage-limit message, got %+v", rep)
	}
	assertPreviousReportKept(t, db, work, good, rep)
}

func TestAnalyzeGroupPromptEchoThenAnswer(t *testing.T) {
	// A healthy codex run: echo, then the answer. The answer must win even
	// though the echoed template comes first, and wording in the prompt must
	// not trigger the rate-limit markers.
	db, mailsRoot, agentRoot, address := newTestIndex(t)
	if err := models.UpsertGroup(db, &models.BounceGroup{
		GroupKey: testGroupKey, Category: CategoryRateLimited,
		UnitValue: "203.0.113.5", Authority: "example.net", Actionable: true,
		RecipientDomain: "example.net", StatusCode: "4.7.0",
		DiagnosticTemplate: "421 4.7.0 too many requests; rate limit exceeded, try again later", Responsible: ResponsibleSender,
	}); err != nil {
		t.Fatal(err)
	}
	answer := "codex\n" + ReportBegin + "\n" + sampleReport + "\n" + ReportEnd + "\n" +
		MetaBegin + "\n{\"summary\":\"流量制限です。\",\"responsible\":\"sender\",\"severity\":\"medium\"}\n" + MetaEnd + "\n"
	registerFake(t, fakeProvider{name: "fake", command: writeEchoingFakeCLI(t, t.TempDir(), answer)})
	rep, err := AnalyzeGroup(context.Background(), AnalyzeInput{
		MailsRoot: mailsRoot, AgentRoot: agentRoot, Address: address, Index: db, GroupKey: testGroupKey, Provider: "fake",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "completed" || rep.ReportMarkdown != sampleReport || rep.Summary != "流量制限です。" || rep.Severity != SeverityMedium {
		t.Fatalf("answer after the echo must be stored: %+v", rep)
	}
	// Echo only (the agent stopped without answering and without an error).
	if err := models.SetGroupNeedsAnalysis(db, testGroupKey, true); err != nil {
		t.Fatal(err)
	}
	registerFake(t, fakeProvider{name: "fake", command: writeEchoingFakeCLI(t, t.TempDir(), "\n")})
	rep, err = AnalyzeGroup(context.Background(), AnalyzeInput{
		MailsRoot: mailsRoot, AgentRoot: agentRoot, Address: address, Index: db, GroupKey: testGroupKey, Provider: "fake",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "error" || !strings.Contains(rep.ErrorMessage, "produced no report") {
		t.Fatalf("echo only must be a no-report failure, not a rate limit: %+v", rep)
	}
	completed, err := models.LatestCompletedAgentReport(db, testGroupKey)
	if err != nil || completed.Summary != "流量制限です。" {
		t.Fatalf("previous completed report must survive: %+v %v", completed, err)
	}
}

func TestAnalyzeGroupLaunchFailureAndUnknownProvider(t *testing.T) {
	db, mailsRoot, agentRoot, address := newTestIndex(t)
	if err := models.SetGroupNeedsAnalysis(db, testGroupKey, false); err != nil {
		t.Fatal(err)
	}
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
	if g, _ := models.GetGroup(db, testGroupKey); !g.NeedsAnalysis {
		t.Error("a launch failure must set needs_analysis for the next sync")
	}
	if err := models.SetGroupNeedsAnalysis(db, testGroupKey, false); err != nil {
		t.Fatal(err)
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
	if g, _ := models.GetGroup(db, testGroupKey); !g.NeedsAnalysis {
		t.Error("an unknown-provider failure must set needs_analysis for the next sync")
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
	if g, _ := models.GetGroup(db, testGroupKey); !g.NeedsAnalysis {
		t.Error("a cancellation must leave needs_analysis set")
	}
	if time.Since(start) > 15*time.Second {
		t.Fatal("cancellation did not stop the run promptly")
	}
}

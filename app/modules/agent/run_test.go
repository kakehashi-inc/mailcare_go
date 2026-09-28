package agent

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

const testGroupKey = "0123456789abcdef"

// newTestIndex creates a temporary index with one actionable group of two
// bounce messages of the same pattern (with their notices on disk where the
// mail engine keeps them) and returns the index plus the roots.
func newTestIndex(t *testing.T) (db *sql.DB, mailsRoot, agentRoot, address string) {
	t.Helper()
	base := t.TempDir()
	mailsRoot = filepath.Join(base, "mails")
	agentRoot = filepath.Join(base, "agent")
	address = "bounce@example.com"
	db, err := models.OpenMailIndex(filepath.Join(mailsRoot, address+".sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := models.UpsertGroup(db, &models.BounceGroup{
		GroupKey: testGroupKey, Category: CategoryUnknownFailure,
		UnitValue: "example.net", Authority: "5.7.1 550 5.7.1 message rejected by policy", Actionable: true,
		RecipientDomain: "example.net", StatusCode: "5.7.1",
		DiagnosticTemplate: "550 5.7.1 message rejected by policy", Responsible: ResponsibleUnknown,
	}); err != nil {
		t.Fatal(err)
	}
	for i, key := range []string{"20260901-120000_aaaaaaaaaaaa", "20260902-120000_bbbbbbbbbbbb"} {
		addTestBounce(t, db, mailsRoot, address, key, uint32(i+1), "pa")
	}
	return db, mailsRoot, agentRoot, address
}

// addTestBounce stores the HTML-only notice of evidence_test.go under key
// and indexes it as a bounce of the test group with the given pattern.
func addTestBounce(t *testing.T, db *sql.DB, mailsRoot, address, key string, uid uint32, pattern string) {
	t.Helper()
	path := mailengine.MessageFilePath(mailsRoot, address, key, "eml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(noticeHTMLOnly()), 0o600); err != nil {
		t.Fatal(err)
	}
	day, _ := strconv.Atoi(key[6:8])
	m := &models.Message{
		MessageKey: key, UID: uid, UIDValidity: 1, Folder: "INBOX", Subject: "Undelivered Mail",
		FromAddress: "mailer-daemon@example.com", Date: time.Date(2026, 9, day, 12, 0, 0, 0, time.UTC),
		TextCount: 1, HTMLCount: 1, BodySource: "text", IsBounce: true, BounceKind: "failed", GroupKey: testGroupKey,
	}
	if err := models.InsertMessage(db, m); err != nil {
		t.Fatal(err)
	}
	if err := models.UpsertBounce(db, &models.Bounce{
		ID: m.ID, GroupKey: testGroupKey, Recipient: "user" + key[len(key)-1:] + "@example.net", RecipientDomain: "example.net",
		Action: "failed", StatusCode: "5.7.1", SMTPCode: "550", Diagnostic: "550 5.7.1 Message rejected by policy",
		DiagnosticTemplate: "550 5.7.1 message rejected by policy", DiagnosticSource: "html:1", CategoryRule: "unknown_failure",
		PatternKey: pattern, RemoteMTA: "mx.example.net", RemoteIP: "192.0.2.10",
	}); err != nil {
		t.Fatal(err)
	}
	// Counters (message count, recipients, IPs, first/last seen) are derived
	// from the inserted messages the same way the mail engine does it.
	if err := models.RefreshGroupCounters(db, testGroupKey); err != nil {
		t.Fatal(err)
	}
}

const cannedSuccess = "OpenAI Codex v0\nsession id: 1234\n" +
	ReportBegin + "\n" + sampleReport + "\n" + ReportEnd + "\n" +
	MetaBegin + "\n{\"summary\":\"宛先が存在しません。\",\"responsible\":\"recipient\",\"severity\":\"low\",\"confidence\":\"low\"}\n" + MetaEnd + "\n"

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
	if rep.Summary != "宛先が存在しません。" || rep.Responsible != ResponsibleRecipient || rep.Severity != SeverityLow ||
		rep.Confidence != ConfidenceLow || rep.ReportMarkdown != sampleReport {
		t.Fatalf("unexpected extracted values: %+v", rep)
	}

	stored, err := models.LatestCompletedAgentReport(db, testGroupKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != rep.ID || stored.ReportMarkdown != sampleReport || !stored.FinishedAt.Valid || stored.Confidence != ConfidenceLow {
		t.Fatalf("stored report differs: %+v", stored)
	}
	// The fake CLI reports no usage: everything stays unknown.
	if stored.Model != "" || stored.ReasoningEffort != "" || stored.TokensUsed.Valid || stored.CommandCount.Valid {
		t.Errorf("usage of a CLI that reports none: %+v", stored)
	}
	// The report records the patterns it covered.
	if covered, err := models.ListAgentReportPatterns(db, rep.ID); err != nil || !slices.Equal(covered, []string{"pa"}) {
		t.Errorf("covered patterns = %v (err %v)", covered, err)
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

	// The workspace of the run is data/agent/<address>/<group_key>/<report_id>/.
	work := RunDir(agentRoot, address, testGroupKey, rep.ID)
	if work != filepath.Join(agentRoot, address, testGroupKey, strconv.FormatInt(rep.ID, 10)) {
		t.Fatalf("unexpected run directory %s", work)
	}
	for _, f := range []string{PromptFileName, ResultFileName, ReportFileName, "AGENTS.md", filepath.Join("sub", "notes.txt"), "received_prompt.txt",
		filepath.Join(EvidenceDirName, "20260902-120000_bbbbbbbbbbbb.txt")} {
		if _, err := os.Stat(filepath.Join(work, f)); err != nil {
			t.Errorf("workspace file %s missing: %v", f, err)
		}
	}
	// The progress names the run directory relative to the address only;
	// the absolute path stays out of the job history shown to every user.
	if !slices.Contains(lines, "workspace "+testGroupKey+"/"+strconv.FormatInt(rep.ID, 10)) {
		t.Errorf("progress must name the run directory: %v", lines)
	}
	for _, l := range lines {
		if strings.Contains(l, agentRoot) {
			t.Errorf("progress must not contain the absolute workspace path: %q", l)
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
	// One pattern, sampled by its newest notice; the mail files themselves
	// are not named.
	if strings.Contains(string(prompt), mailsRoot) {
		t.Error("the prompt must not name the mail files")
	}
	if _, err := os.Stat(filepath.Join(work, EvidenceDirName, "20260901-120000_aaaaaaaaaaaa.txt")); err == nil {
		t.Error("only the newest notice of a pattern is a sample")
	}
	for _, want := range []string{
		"Category: unknown_failure - ", "Action unit (unit_value): example.net - ",
		"Actionable by the mail administrator: yes",
		"Messages: 2 in 1 patterns.", "[S1] pattern P1, message 20260902-120000_bbbbbbbbbbbb",
		"| Remote server said: 550 5.7.1 Message rejected by policy",
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
	if !slices.ContainsFunc(lines, func(l string) bool {
		return strings.HasPrefix(l, "running ") && strings.HasSuffix(l, " on 2 messages in 1 patterns, 1 samples")
	}) {
		t.Errorf("progress must announce the run: %v", lines)
	}
}

// usageFake is a fake provider that reads its usage like codex does.
type usageFake struct{ fakeProvider }

func (usageFake) ParseUsage(raw, answer string) models.AgentRunUsage {
	return codexProvider{}.ParseUsage(raw, answer)
}

func TestAnalyzeGroupPromptWrittenAfterRunAndUsage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	db, mailsRoot, agentRoot, address := newTestIndex(t)
	dir := t.TempDir()
	answer := "--------\nmodel: gpt-test\nreasoning effort: high\n--------\ncodex\nexec\ncat evidence/x.txt\n" +
		cannedSuccess + "tokens used\n12,345\n"
	cannedPath := filepath.Join(dir, "canned.txt")
	if err := os.WriteFile(cannedPath, []byte(answer), 0o600); err != nil {
		t.Fatal(err)
	}
	// The CLI records whether PROMPT.md and the evidence file exist while it
	// runs.
	script := filepath.Join(dir, "cli.sh")
	body := "#!/bin/sh\ncat > /dev/null\n" +
		"if [ -e " + PromptFileName + " ]; then echo present > saw_prompt.txt; else echo absent > saw_prompt.txt; fi\n" +
		"ls " + EvidenceDirName + " > saw_evidence.txt\ncat \"" + cannedPath + "\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	registerFake(t, usageFake{fakeProvider{name: "fake", command: []string{script}}})
	rep, err := AnalyzeGroup(context.Background(), AnalyzeInput{
		MailsRoot: mailsRoot, AgentRoot: agentRoot, Address: address, Index: db, GroupKey: testGroupKey, Provider: "fake",
	}, nil)
	if err != nil || rep.Status != "completed" {
		t.Fatalf("run: %+v %v", rep, err)
	}
	work := RunDir(agentRoot, address, testGroupKey, rep.ID)
	if saw, _ := os.ReadFile(filepath.Join(work, "saw_prompt.txt")); strings.TrimSpace(string(saw)) != "absent" {
		t.Errorf("PROMPT.md must not exist while the CLI runs (saw %q)", saw)
	}
	if saw, _ := os.ReadFile(filepath.Join(work, "saw_evidence.txt")); !strings.Contains(string(saw), "20260902-120000_bbbbbbbbbbbb.txt") {
		t.Errorf("the evidence file must exist while the CLI runs (saw %q)", saw)
	}
	if _, err := os.Stat(filepath.Join(work, PromptFileName)); err != nil {
		t.Errorf("PROMPT.md must be kept after the run: %v", err)
	}
	stored, err := models.LatestCompletedAgentReport(db, testGroupKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Model != "gpt-test" || stored.ReasoningEffort != "high" || stored.TokensUsed.Int64 != 12345 || stored.CommandCount.Int64 != 1 {
		t.Errorf("usage not recorded: %+v", stored)
	}
	result, _ := os.ReadFile(filepath.Join(work, ResultFileName))
	if !strings.Contains(string(result), "Tokens used: 12345\n") || !strings.Contains(string(result), "Commands run: 1\n") {
		t.Errorf("RESULT.log must carry the usage:\n%s", result)
	}
}

func TestAnalyzeGroupUpdate(t *testing.T) {
	db, mailsRoot, agentRoot, address := newTestIndex(t)
	registerFake(t, fakeProvider{name: "fake", command: writeFakeCLI(t, t.TempDir(), cannedSuccess)})
	in := AnalyzeInput{MailsRoot: mailsRoot, AgentRoot: agentRoot, Address: address, Index: db, GroupKey: testGroupKey, Provider: "fake"}
	first, err := AnalyzeGroup(context.Background(), in, nil)
	if err != nil || first.Status != "completed" {
		t.Fatalf("first run: %+v %v", first, err)
	}
	// A notice of a new pattern arrives: only it is sampled, the previous
	// report is quoted.
	addTestBounce(t, db, mailsRoot, address, "20260903-120000_cccccccccccc", 3, "pb")
	registerFake(t, fakeProvider{name: "fake", command: writeEchoingFakeCLI(t, t.TempDir(), cannedSuccess)})
	second, err := AnalyzeGroup(context.Background(), in, nil)
	if err != nil || second.Status != "completed" {
		t.Fatalf("second run: %+v %v", second, err)
	}
	work := RunDir(agentRoot, address, testGroupKey, second.ID)
	prompt, _ := os.ReadFile(filepath.Join(work, PromptFileName))
	for _, want := range []string{"This is an UPDATE of the PREVIOUS REPORT below", "=== PREVIOUS REPORT", "Summary: 宛先が存在しません。",
		"[new]", "[covered]", "[S1] pattern P", "message 20260903-120000_cccccccccccc"} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("update prompt lacks %q", want)
		}
	}
	if strings.Count(string(prompt), "\n[S") != 1 {
		t.Error("only the new pattern gets a sample")
	}
	if covered, _ := models.ListAgentReportPatterns(db, second.ID); !slices.Equal(covered, []string{"pa", "pb"}) {
		t.Errorf("the update covers every pattern, got %v", covered)
	}
	if g, _ := models.GetGroup(db, testGroupKey); g.NeedsAnalysis {
		t.Error("every pattern is covered: needs_analysis must be clear")
	}
}

func TestAnalyzeGroupRecipientSideFailureKeepsFlagClear(t *testing.T) {
	db, mailsRoot, agentRoot, address := newTestIndex(t)
	if err := models.UpsertGroup(db, &models.BounceGroup{
		GroupKey: testGroupKey, Category: CategoryUserUnknown, UnitValue: "user2@example.net", Actionable: false,
		RecipientDomain: "example.net", StatusCode: "5.1.1", Responsible: ResponsibleRecipient,
	}); err != nil {
		t.Fatal(err)
	}
	if err := models.RefreshGroupCounters(db, testGroupKey); err != nil {
		t.Fatal(err)
	}
	registerFake(t, fakeProvider{name: "fake", command: writeFakeCLI(t, t.TempDir(), "I refuse to answer.\n")})
	rep, err := AnalyzeGroup(context.Background(), AnalyzeInput{
		MailsRoot: mailsRoot, AgentRoot: agentRoot, Address: address, Index: db, GroupKey: testGroupKey, Provider: "fake",
	}, nil)
	if err != nil || rep.Status != "error" {
		t.Fatalf("expected a failure: %+v %v", rep, err)
	}
	if g, _ := models.GetGroup(db, testGroupKey); g.NeedsAnalysis {
		t.Error("a recipient-side group is not analyzed automatically: its flag must stay clear")
	}
	// A successful run of a recipient-side group works the same way.
	registerFake(t, fakeProvider{name: "fake", command: writeFakeCLI(t, t.TempDir(), cannedSuccess)})
	rep, err = AnalyzeGroup(context.Background(), AnalyzeInput{
		MailsRoot: mailsRoot, AgentRoot: agentRoot, Address: address, Index: db, GroupKey: testGroupKey, Provider: "fake",
	}, nil)
	if err != nil || rep.Status != "completed" {
		t.Fatalf("recipient-side run: %+v %v", rep, err)
	}
	work := RunDir(agentRoot, address, testGroupKey, rep.ID)
	prompt, _ := os.ReadFile(filepath.Join(work, PromptFileName))
	if !strings.Contains(string(prompt), "This group is NOT actionable") {
		t.Error("a recipient-side group gets the recipient-side task")
	}
	if g, _ := models.GetGroup(db, testGroupKey); g.NeedsAnalysis {
		t.Error("needs_analysis of a recipient-side group must stay clear")
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
	work := RunDir(agentRoot, address, testGroupKey, rep.ID)
	result, _ := os.ReadFile(filepath.Join(work, ResultFileName))
	if !strings.HasPrefix(string(result), "Result: Failure\nReason: ") {
		t.Errorf("RESULT.log content unexpected:\n%s", result)
	}
	if _, err := os.Stat(filepath.Join(work, ReportFileName)); err == nil {
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
		{"secret in the report", ReportBegin + "\n" + sampleReport + "\nkey: " + strings.Repeat("0f", 32) + "\n" + ReportEnd + "\n", "secret-like content"},
		{"secret in the summary", ReportBegin + "\n" + sampleReport + "\n" + ReportEnd + "\n" +
			MetaBegin + "\n{\"summary\":\"token mlc_" + strings.Repeat("ab", 20) + "\",\"responsible\":\"sender\",\"severity\":\"low\"}\n" + MetaEnd + "\n", "secret-like content"},
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
			// Each run has its own directory: the good run keeps its REPORT.md,
			// the failed run has none.
			goodWork := RunDir(agentRoot, address, testGroupKey, good.ID)
			report, _ := os.ReadFile(filepath.Join(goodWork, ReportFileName))
			if strings.TrimSpace(string(report)) != sampleReport {
				t.Errorf("REPORT.md of the good run must be untouched:\n%s", report)
			}
			work := RunDir(agentRoot, address, testGroupKey, rep.ID)
			if work == goodWork {
				t.Fatal("the failed run must get its own directory")
			}
			if _, err := os.Stat(filepath.Join(work, ReportFileName)); err == nil {
				t.Error("REPORT.md must not be written by the failed run")
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
// the CLI given by argv and returns both reports with their run directories.
func analyzeGoodThenFailure(t *testing.T, argv []string) (db *sql.DB, goodWork, failedWork string, good, second *models.AgentReport) {
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
	return db, RunDir(agentRoot, address, testGroupKey, good.ID), RunDir(agentRoot, address, testGroupKey, second.ID), good, second
}

// assertPreviousReportKept checks that a failed run left the previous good
// report as the latest completed one, the REPORT.md of the good run
// untouched (and none in its own directory) and the group still flagged for
// analysis.
func assertPreviousReportKept(t *testing.T, db *sql.DB, goodWork, failedWork string, good, failed *models.AgentReport) {
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
	report, _ := os.ReadFile(filepath.Join(goodWork, ReportFileName))
	if strings.TrimSpace(string(report)) != sampleReport {
		t.Errorf("REPORT.md of the good run must be untouched:\n%s", report)
	}
	if _, err := os.Stat(filepath.Join(failedWork, ReportFileName)); err == nil {
		t.Error("REPORT.md must not be written by the failed run")
	}
	if _, err := os.Stat(filepath.Join(failedWork, ResultFileName)); err != nil {
		t.Errorf("RESULT.log of the failed run missing: %v", err)
	}
}

func TestAnalyzeGroupUsageLimitAfterPromptEcho(t *testing.T) {
	// The real codex failure: the prompt is echoed (so the transcript holds
	// the template markers), then the usage-limit error, and nothing else.
	db, goodWork, work, good, rep := analyzeGoodThenFailure(t, writeEchoingFakeCLI(t, t.TempDir(), "\n"+codexUsageLimit+codexUsageLimit))
	if rep.ErrorMessage != "usage limit reached (retry after 7:22 PM)" {
		t.Fatalf("expected the usage-limit message, got %+v", rep)
	}
	assertPreviousReportKept(t, db, goodWork, work, good, rep)
	result, _ := os.ReadFile(filepath.Join(work, ResultFileName))
	if !strings.HasPrefix(string(result), "Result: Failure\nReason: usage limit reached (retry after 7:22 PM)\n") ||
		!strings.Contains(string(result), "=== CONSTRAINTS ===") || !strings.Contains(string(result), strings.TrimSpace(codexUsageLimit)) {
		t.Errorf("RESULT.log must carry the reason and the full transcript including the echo:\n%s", result)
	}
}

func TestAnalyzeGroupUsageLimitWithoutEcho(t *testing.T) {
	db, goodWork, work, good, rep := analyzeGoodThenFailure(t, writeFakeCLI(t, t.TempDir(), "Error: HTTP 429 Too Many Requests\n"))
	if rep.ErrorMessage != "usage limit reached" {
		t.Fatalf("expected the generic usage-limit message, got %+v", rep)
	}
	assertPreviousReportKept(t, db, goodWork, work, good, rep)
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

	if _, err := AnalyzeGroup(context.Background(), AnalyzeInput{AgentRoot: agentRoot, Index: db, GroupKey: "missing", Provider: "ghost"}, nil); err == nil {
		t.Fatal("a missing group cannot be recorded and must return an error")
	}
	if n, _ := models.ListAgentReports(db, "missing"); len(n) != 0 {
		t.Fatal("no report row may exist for a missing group")
	}
	// Without a workspace root nothing is recorded either.
	before, _ := models.ListAgentReports(db, testGroupKey)
	if _, err := AnalyzeGroup(context.Background(), AnalyzeInput{Index: db, GroupKey: testGroupKey, Provider: "ghost"}, nil); err == nil {
		t.Fatal("a missing workspace root must be refused")
	}
	if after, _ := models.ListAgentReports(db, testGroupKey); len(after) != len(before) {
		t.Fatal("no report row may be added when the workspace root is missing")
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

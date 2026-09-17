package agent

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
)

// stubMessageFilePath points messageFilePath at <root>/<address>/<key>.<ext>
// for the duration of the test (the mailengine implementation may still be a
// stub while this package is developed).
func stubMessageFilePath(t *testing.T) {
	t.Helper()
	prev := messageFilePath
	messageFilePath = func(root, address, key, ext string) string {
		return filepath.Join(root, address, key+"."+ext)
	}
	t.Cleanup(func() { messageFilePath = prev })
}

func samplePromptInput(t *testing.T, mailsRoot string) PromptInput {
	t.Helper()
	addr := "bounce@example.com"
	dir := filepath.Join(mailsRoot, addr)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Two messages: the first has a .txt, the second only an .eml.
	for _, name := range []string{"20260901-120000_aaaaaaaaaaaa.eml", "20260901-120000_aaaaaaaaaaaa.txt", "20260902-120000_bbbbbbbbbbbb.eml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	recipients := make([]string, 0, 35)
	for i := 0; i < 35; i++ {
		recipients = append(recipients, fmt.Sprintf("user%02d@example.net", i))
	}
	first := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	last := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	return PromptInput{
		MailsRoot: mailsRoot,
		Address:   addr,
		Group: &models.BounceGroup{
			GroupKey:           "abcdef0123456789",
			Title:              "example.net - 5.1.1 - user unknown",
			BounceKind:         "failed",
			RecipientDomain:    "example.net",
			StatusCode:         "5.1.1",
			SMTPCode:           "550",
			DiagnosticTemplate: "550 5.1.1 <addr>: recipient address rejected: user unknown",
			Responsible:        ResponsibleRecipient,
			MessageCount:       2,
			RecipientCount:     35,
			RemoteIPCount:      1,
			FirstSeen:          sql.NullTime{Time: first, Valid: true},
			LastSeen:           sql.NullTime{Time: last, Valid: true},
		},
		Stats: &models.GroupBounceStats{Recipients: recipients, RemoteIPs: []string{"192.0.2.10"}, RemoteMTAs: []string{"mx.example.net"}},
		Messages: []*models.Message{
			{MessageKey: "20260902-120000_bbbbbbbbbbbb"},
			{MessageKey: "20260901-120000_aaaaaaaaaaaa"},
		},
	}
}

func TestBuildPromptJapanese(t *testing.T) {
	stubMessageFilePath(t)
	root := t.TempDir()
	in := samplePromptInput(t, root)
	p := BuildPrompt(in)

	for _, want := range []string{
		ReportBegin, ReportEnd, MetaBegin, MetaEnd,
		"## 原因の分析", "## 影響範囲", "## 推奨する対応", "## 対応すべき担当",
		"Write the REPORT in Japanese",
		"READ-ONLY", "UNTRUSTED DATA", "No network access",
		"Title: example.net - 5.1.1 - user unknown",
		"Bounce kind: failed", "Recipient domain: example.net", "Status code: 5.1.1", "SMTP code: 550",
		"Diagnostic template: 550 5.1.1 <addr>: recipient address rejected: user unknown",
		"Messages: 2", "Distinct recipients: 35", "Distinct remote IPs: 1",
		"First seen: 2026-09-01T12:00:00Z", "Last seen: 2026-09-02T12:00:00Z",
		"Responsible (machine guess): recipient",
		"Recipients (35): user00@example.net", "user29@example.net, ... (5 more)",
		"Remote IPs (1): 192.0.2.10", "Remote MTAs (1): mx.example.net",
		"- " + filepath.Join(root, in.Address, "20260902-120000_bbbbbbbbbbbb.eml") + "\n",
		"- " + filepath.Join(root, in.Address, "20260901-120000_aaaaaaaaaaaa.eml") + "\n  text: " + filepath.Join(root, in.Address, "20260901-120000_aaaaaaaaaaaa.txt") + "\n",
		"Output nothing after the last marker.",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q\n%s", want, p)
		}
	}
	if strings.Contains(p, "user30@example.net") {
		t.Error("recipients must be capped at MaxPromptRecipients")
	}
	if strings.Contains(p, "20260902-120000_bbbbbbbbbbbb.txt") {
		t.Error("a missing .txt must not be listed")
	}
	// Section order: constraints, summary, files, output.
	idx := func(s string) int { return strings.Index(p, s) }
	if !(idx("=== CONSTRAINTS ===") < idx("=== GROUP SUMMARY") && idx("=== GROUP SUMMARY") < idx("=== MAIL FILES") && idx("=== MAIL FILES") < idx("=== OUTPUT")) {
		t.Error("sections out of order")
	}
}

func TestBuildPromptEnglishAndFallback(t *testing.T) {
	stubMessageFilePath(t)
	in := samplePromptInput(t, t.TempDir())
	in.Language = "en"
	p := BuildPrompt(in)
	if !strings.Contains(p, "## Cause analysis") || !strings.Contains(p, "Write the REPORT in English") {
		t.Fatalf("english headings missing:\n%s", p)
	}
	in.Language = "xx"
	if !strings.Contains(BuildPrompt(in), "## 原因の分析") {
		t.Fatal("unknown language must fall back to Japanese")
	}
	in.Messages = nil
	in.Stats = nil
	if !strings.Contains(BuildPrompt(in), "(none)") {
		t.Fatal("empty message list must print (none)")
	}
}

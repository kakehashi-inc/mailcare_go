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
			Title:              "ip_blocked: 203.0.113.5 @ spamhaus.org",
			Category:           CategoryIPBlocked,
			UnitValue:          "203.0.113.5",
			Authority:          "spamhaus.org",
			Actionable:         true,
			BounceKind:         "failed",
			RecipientDomain:    "example.net",
			StatusCode:         "5.7.1",
			SMTPCode:           "554",
			DiagnosticTemplate: "554 5.7.1 service unavailable; client host [<ip>] blocked using zen.spamhaus.org",
			Responsible:        ResponsibleSender,
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
		"This group is ACTIONABLE by the mail administrator.",
		"delisting steps for the named blacklist",
		"Category: ip_blocked - " + CategoryGlossary[CategoryIPBlocked].Description,
		"Action unit (unit_value): 203.0.113.5 - the sending IP address that is blocked",
		"Authority: spamhaus.org - the blacklist provider that lists the IP",
		"Actionable by the mail administrator: yes",
		"Title: ip_blocked: 203.0.113.5 @ spamhaus.org",
		"Bounce kind: failed", "Recipient domain: example.net", "Status code: 5.7.1", "SMTP code: 554",
		"Diagnostic template: 554 5.7.1 service unavailable; client host [<ip>] blocked using zen.spamhaus.org",
		"Messages: 2", "Distinct recipients: 35", "Distinct remote IPs: 1",
		"First seen: 2026-09-01T12:00:00Z", "Last seen: 2026-09-02T12:00:00Z",
		"Responsible (machine guess): sender",
		"1. <concrete action the mail administrator takes for the action unit>",
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
	if strings.Contains(p, "NOT actionable") {
		t.Error("an actionable group must not get the recipient-side instructions")
	}
	// Section order: constraints, task, summary, files, output.
	idx := func(s string) int { return strings.Index(p, s) }
	if !(idx("=== CONSTRAINTS ===") < idx("=== TASK ===") && idx("=== TASK ===") < idx("=== GROUP SUMMARY") && idx("=== GROUP SUMMARY") < idx("=== MAIL FILES") && idx("=== MAIL FILES") < idx("=== OUTPUT")) {
		t.Error("sections out of order")
	}
	// The summary leads with the category, unit, authority and actionability
	// before the descriptive columns.
	if !(idx("Mailbox: ") < idx("Category: ") && idx("Category: ") < idx("Action unit (unit_value): ") &&
		idx("Action unit (unit_value): ") < idx("Authority: ") && idx("Authority: ") < idx("Actionable by the mail administrator: ") &&
		idx("Actionable by the mail administrator: ") < idx("Title: ")) {
		t.Error("group summary must lead with category, unit, authority and actionability")
	}
}

func TestBuildPromptNotActionable(t *testing.T) {
	stubMessageFilePath(t)
	in := samplePromptInput(t, t.TempDir())
	in.Group.Category = CategoryUserUnknown
	in.Group.UnitValue = "alice@example.net"
	in.Group.Authority = ""
	in.Group.Actionable = false
	in.Group.Title = "user_unknown: alice@example.net"
	p := BuildPrompt(in)
	for _, want := range []string{
		"This group is NOT actionable by the mail administrator",
		"what to tell the recipient-side owner",
		CategoryGlossary[CategoryUserUnknown].Guidance,
		"Category: user_unknown - " + CategoryGlossary[CategoryUserUnknown].Description,
		"Action unit (unit_value): alice@example.net - the recipient address that does not exist",
		"Actionable by the mail administrator: no (recipient-side problem)",
		"1. <short note on what to tell the recipient-side owner>",
		ReportBegin, ReportEnd, MetaBegin, MetaEnd,
		"## 原因の分析", "## 影響範囲", "## 推奨する対応", "## 対応すべき担当",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q\n%s", want, p)
		}
	}
	for _, unwanted := range []string{"Authority:", "This group is ACTIONABLE", "<next action>"} {
		if strings.Contains(p, unwanted) {
			t.Errorf("prompt must not contain %q for a recipient-side group\n%s", unwanted, p)
		}
	}
}

func TestBuildPromptUnknownCategory(t *testing.T) {
	stubMessageFilePath(t)
	in := samplePromptInput(t, t.TempDir())
	in.Group.Category = ""
	in.Group.UnitValue = ""
	in.Group.Authority = ""
	in.Group.Actionable = true
	p := BuildPrompt(in)
	for _, want := range []string{
		"Category: (not classified) - " + unknownCategory.Description,
		"Actionable by the mail administrator: yes",
		"This group is ACTIONABLE by the mail administrator",
		unknownCategory.Guidance,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q\n%s", want, p)
		}
	}
	if strings.Contains(p, "Action unit (unit_value):") || strings.Contains(p, "Authority:") {
		t.Errorf("empty unit and authority must be skipped\n%s", p)
	}
	in.Group.Category = "  IP_Blocked "
	if !strings.Contains(BuildPrompt(in), "Category: IP_Blocked - "+CategoryGlossary[CategoryIPBlocked].Description) {
		t.Error("category lookup must be case- and space-insensitive")
	}
}

func TestCategoryGlossaryComplete(t *testing.T) {
	for name, info := range CategoryGlossary {
		if info.Description == "" || info.Unit == "" || info.Guidance == "" {
			t.Errorf("%s: description, unit and guidance are required", name)
		}
		if info.Actionable && info.Authority == "" {
			t.Errorf("%s: an actionable category names its authority", name)
		}
		if !info.Actionable && info.Authority != "" {
			t.Errorf("%s: a recipient-side category has no authority", name)
		}
	}
	for _, name := range []string{
		CategoryIPBlocked, CategoryRateLimited, CategorySenderBlocked, CategoryAuthFailure, CategoryContentRejected,
		CategoryMessageTooLarge, CategoryServerConfig, CategoryUnknownFailure, CategoryUserUnknown, CategoryMailboxFull,
		CategoryMailboxDisabled, CategoryDomainNotFound, CategoryDeliveryDelay,
	} {
		if _, ok := CategoryGlossary[name]; !ok {
			t.Errorf("%s missing from the glossary", name)
		}
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

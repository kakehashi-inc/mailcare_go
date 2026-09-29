package pdfreport

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules"
)

// fakeTranslate answers "<key>" with the params appended, and nothing for
// the keys of unknown categories.
func fakeTranslate(key string, params map[string]any) (string, bool) {
	if strings.HasPrefix(key, "value.category.nosuch.") {
		return "", false
	}
	if len(params) == 0 {
		return key, true
	}
	return fmt.Sprintf("%s %v", key, params), true
}

func TestMain(m *testing.M) {
	modules.EmbeddedFS = os.DirFS(filepath.Join("..", "..", "..", "embedded"))
	os.Exit(m.Run())
}

func TestWrapKeepsEveryCharacter(t *testing.T) {
	d, err := newDocument("footer")
	if err != nil {
		t.Fatal(err)
	}
	d.setFont(familyText, "", 10)
	text := "宛先アドレスが存在しません。宛先リストから該当アドレスを除外するか、相手に正しいアドレスを確認してください。" +
		"The recipient address does not exist, remove it from the list.\n2 行目"
	lines := d.wrap(text, 40)
	if len(lines) < 4 {
		t.Fatalf("lines = %q, want the text wrapped", lines)
	}
	joined := strings.Join(lines, "")
	want := strings.ReplaceAll(strings.ReplaceAll(text, "\n", ""), " ", "")
	if strings.ReplaceAll(joined, " ", "") != want {
		t.Errorf("wrapping lost characters:\n%q\n%q", joined, want)
	}
	for _, l := range lines {
		if w := d.textWidth(l); w > 40.001 {
			t.Errorf("line %q is %.1fmm wide", l, w)
		}
		if r := []rune(l); len(r) > 0 && strings.ContainsRune(noLineStart, r[0]) {
			t.Errorf("line %q starts with closing punctuation", l)
		}
	}
	if lines[len(lines)-1] != "2 行目" {
		t.Errorf("last line = %q, want the line after the line feed", lines[len(lines)-1])
	}
}

func TestWriteGroupReport(t *testing.T) {
	var recipients []string
	for i := range 150 {
		recipients = append(recipients, fmt.Sprintf("user%03d@example.jp", i))
	}
	g := &models.BounceGroup{GroupKey: "0123456789abcdef", Category: "user_unknown", UnitValue: "a@example.jp",
		RecipientDomain: "example.jp", StatusCode: "5.1.1", DiagnosticTemplate: "550 5.1.1 <host>; 宛先がありません",
		Responsible: "recipient", State: "open", MessageCount: 3,
		FirstSeen: sql.NullTime{Time: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Valid: true}}
	var out bytes.Buffer
	err := WriteGroupReport(&out, &GroupReport{Mailbox: "ops@example.jp", Group: g,
		Stats:       &models.GroupBounceStats{Recipients: recipients},
		GeneratedAt: time.Date(2026, 9, 30, 1, 2, 0, 0, time.UTC), Location: time.UTC, Language: "ja", Creator: "MailCare test"},
		fakeTranslate)
	if err != nil {
		t.Fatal(err)
	}
	pdf := out.Bytes()
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("not a PDF: %q", pdf[:min(16, len(pdf))])
	}
	pages := regexp.MustCompile(`/Type /Page\b[^s]`).FindAll(pdf, -1)
	if len(pages) < 2 {
		t.Errorf("%d pages, want the 150 recipients to continue on a second page", len(pages))
	}
	// The fonts are embedded as subsets, so the file stays small.
	if len(pdf) > 400_000 {
		t.Errorf("PDF is %d bytes", len(pdf))
	}
}

func TestGroupHeadline(t *testing.T) {
	g := &models.BounceGroup{Category: "ip_blocked", UnitValue: "203.0.113.5", Authority: "spamhaus.org"}
	if got := GroupHeadline(g, fakeTranslate); !strings.HasPrefix(got, "field.group.titleWithAuthority") {
		t.Errorf("with authority: %q", got)
	}
	g = &models.BounceGroup{Category: "nosuch", RecipientDomain: "example.jp"}
	if got := GroupHeadline(g, fakeTranslate); !strings.Contains(got, "label:nosuch") || !strings.Contains(got, "where:example.jp") {
		t.Errorf("unknown category: %q", got)
	}
	g = &models.BounceGroup{Category: "unknown_failure", UnitValue: "x", Authority: "5.0.0 ..."}
	if got := GroupHeadline(g, fakeTranslate); !strings.HasPrefix(got, "field.group.title ") {
		t.Errorf("unknown_failure keeps no authority: %q", got)
	}
}

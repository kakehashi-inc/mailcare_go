package agent

import (
	"strings"
	"testing"
)

const sampleReport = "## 原因の分析\n宛先アドレスが存在しません。\n\n## 影響範囲\n1 件。\n\n## 推奨する対応\n1. 削除する\n\n## 対応すべき担当\n宛先アドレスの管理者"

func TestParseOutputBothBlocks(t *testing.T) {
	raw := "codex header\n" + ReportBegin + "\n" + sampleReport + "\n" + ReportEnd + "\n" +
		MetaBegin + "\n{\"summary\":\"存在しない宛先です。\",\"responsible\":\"Recipient\",\"severity\":\"low\"}\n" + MetaEnd + "\ntrailing"
	out := ParseOutput(raw)
	if !out.ReportParsed || out.Report != sampleReport {
		t.Fatalf("report not extracted: parsed=%v report=%q", out.ReportParsed, out.Report)
	}
	if !out.MetaParsed {
		t.Fatal("META should parse")
	}
	if out.Meta.Summary != "存在しない宛先です。" || out.Meta.Responsible != ResponsibleRecipient || out.Meta.Severity != SeverityLow {
		t.Fatalf("unexpected meta: %+v", out.Meta)
	}
}

func TestParseOutputLastReportWins(t *testing.T) {
	raw := ReportBegin + "\necho of the prompt\n" + ReportEnd + "\n" + ReportBegin + "\n" + sampleReport + "\n" + ReportEnd
	out := ParseOutput(raw)
	if out.Report != sampleReport {
		t.Fatalf("expected the last block, got %q", out.Report)
	}
}

func TestParseOutputMissingMeta(t *testing.T) {
	raw := ReportBegin + "\n" + sampleReport + "\n" + ReportEnd
	out := ParseOutput(raw)
	if !out.ReportParsed || out.MetaParsed {
		t.Fatalf("parsed flags: report=%v meta=%v", out.ReportParsed, out.MetaParsed)
	}
	if out.Meta.Summary != "宛先アドレスが存在しません。" {
		t.Fatalf("summary should come from the first prose paragraph, got %q", out.Meta.Summary)
	}
	if out.Meta.Responsible != "" || out.Meta.Severity != "" {
		t.Fatalf("enums should be empty: %+v", out.Meta)
	}
}

func TestParseOutputMissingReport(t *testing.T) {
	raw := "some text\n" + MetaBegin + "\n{\"summary\":\"x\"}\n" + MetaEnd
	out := ParseOutput(raw)
	if out.ReportParsed || out.Report != "" {
		t.Fatalf("report should be absent: %+v", out)
	}
	if !out.MetaParsed || out.Meta.Summary != "x" {
		t.Fatalf("meta should still parse: %+v", out)
	}
	if got := ParseOutput(ReportBegin + "\nunterminated"); got.ReportParsed {
		t.Fatal("unterminated block must not count as a report")
	}
}

func TestParseOutputGarbage(t *testing.T) {
	for _, raw := range []string{"", "no markers at all", "<<<MLC:/REPORT>>>\n<<<MLC:REPORT>>>"} {
		out := ParseOutput(raw)
		if out.ReportParsed || out.MetaParsed || out.Report != "" || out.Meta.Summary != "" {
			t.Fatalf("garbage %q parsed as %+v", raw, out)
		}
	}
}

func TestParseOutputMetaTolerance(t *testing.T) {
	cases := []struct {
		name   string
		meta   string
		parsed bool
		want   Meta
	}{
		{"fenced", "```json\n{\"summary\":\"a  b\\nc\",\"responsible\":\"domain\",\"severity\":\"HIGH\"}\n```", true, Meta{Summary: "a b c", Responsible: ResponsibleDomain, Severity: SeverityHigh}},
		{"invalid enums", "{\"summary\":\"s\",\"responsible\":\"nobody\",\"severity\":\"critical\"}", true, Meta{Summary: "s"}},
		{"not json", "summary: nothing here", false, Meta{Summary: "宛先アドレスが存在しません。"}},
		{"array", "[1,2]", false, Meta{Summary: "宛先アドレスが存在しません。"}},
	}
	for _, c := range cases {
		raw := ReportBegin + "\n" + sampleReport + "\n" + ReportEnd + "\n" + MetaBegin + "\n" + c.meta + "\n" + MetaEnd
		out := ParseOutput(raw)
		if out.MetaParsed != c.parsed {
			t.Errorf("%s: MetaParsed=%v want %v", c.name, out.MetaParsed, c.parsed)
		}
		if out.Meta != c.want {
			t.Errorf("%s: meta=%+v want %+v", c.name, out.Meta, c.want)
		}
	}
}

func TestDeriveSummaryTruncates(t *testing.T) {
	long := strings.Repeat("あ", MaxSummaryRunes+50)
	got := deriveSummary("# title\n\n" + long + "\n\nsecond paragraph")
	if n := len([]rune(got)); n != MaxSummaryRunes {
		t.Fatalf("expected %d runes, got %d", MaxSummaryRunes, n)
	}
	if deriveSummary("## only headings\n### more") != "" {
		t.Fatal("headings only must yield an empty summary")
	}
	if got := deriveSummary("line one\nline two\n## next"); got != "line one line two" {
		t.Fatalf("multi-line paragraph folding: %q", got)
	}
}

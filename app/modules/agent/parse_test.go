package agent

import (
	"strings"
	"testing"
)

const sampleReport = "## 原因の分析\n宛先アドレスが宛先サーバーに存在せず、550 5.1.1 で拒否されています。\n\n## 影響範囲\n1 件。\n\n## 推奨する対応\n1. 削除する\n\n## 対応すべき担当\n宛先アドレスの管理者"

// sampleFirstParagraph is the first prose paragraph of sampleReport (the
// summary derived when META carries none).
const sampleFirstParagraph = "宛先アドレスが宛先サーバーに存在せず、550 5.1.1 で拒否されています。"

// echoedTemplate is what an agent produces when it copies the OUTPUT section
// of the prompt instead of answering.
const echoedTemplate = "## 原因の分析\n<what failed and why, citing the evidence in the notices>\n## 影響範囲\n<which recipients, domains or sending paths are affected and since when>\n## 推奨する対応\n1. <concrete action the mail administrator takes for the action unit>\n2. <next action>\n## 対応すべき担当\n<who should act: our sending server admin / the recipient address owner / the recipient domain admin, and why>"

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
	if out.Meta.Summary != sampleFirstParagraph {
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
		{"not json", "summary: nothing here", false, Meta{Summary: sampleFirstParagraph}},
		{"array", "[1,2]", false, Meta{Summary: sampleFirstParagraph}},
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

func TestValidateOutput(t *testing.T) {
	good := ParseOutput(ReportBegin + "\n" + sampleReport + "\n" + ReportEnd + "\n" +
		MetaBegin + "\n{\"summary\":\"存在しない宛先です。\",\"responsible\":\"recipient\",\"severity\":\"low\"}\n" + MetaEnd)
	if err := ValidateOutput(good); err != nil {
		t.Fatalf("a real report must validate: %v", err)
	}
	// A quoted address in angle brackets or a template token like <addr> on
	// its own line is not a placeholder (no space inside the brackets).
	quoted := ParseOutput(ReportBegin + "\n## 原因の分析\n<alice@example.net>\n<addr>\n宛先アドレスが宛先サーバーに存在せず、550 5.1.1 で拒否されています。\n## 推奨する対応\n1. 削除する\n" + ReportEnd)
	if err := ValidateOutput(quoted); err != nil {
		t.Fatalf("angle-bracket tokens without spaces must pass: %v", err)
	}
	if err := ValidateOutput(ParseOutput("no markers")); err == nil {
		t.Fatal("no report block must fail")
	}

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"one placeholder line left", ReportBegin + "\n" + sampleReport + "\n2. <next action>\n" + ReportEnd, "template placeholder \"2. <next action>\""},
		{"placeholder with parenthesis list", ReportBegin + "\n" + sampleReport + "\n3) <who should act: admin>\n" + ReportEnd, "template placeholder"},
		{"headings only", ReportBegin + "\n## 原因の分析\n## 影響範囲\n## 推奨する対応\n## 対応すべき担当\n" + ReportEnd, "too short (0 runes"},
		{"too short", ReportBegin + "\n## 原因の分析\n不明。\n## 推奨する対応\n1. なし\n" + ReportEnd, "too short"},
	}
	for _, c := range cases {
		out := ParseOutput(c.raw)
		if !out.ReportParsed {
			t.Fatalf("%s: fixture must have a report block", c.name)
		}
		err := ValidateOutput(out)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want error containing %q, got %v", c.name, c.want, err)
		}
	}
	// A placeholder summary that slipped through (ParseOutput drops template
	// META blocks, so this is only reachable by hand) is rejected too.
	withPlaceholder := good
	withPlaceholder.Meta.Summary = "<one or two sentences>"
	if err := ValidateOutput(withPlaceholder); err == nil || !strings.Contains(err.Error(), "summary is the template placeholder") {
		t.Errorf("placeholder summary: got %v", err)
	}
}

// templateMeta is the META template as the prompt prints it.
const templateMeta = `{"summary":"<one or two sentences>","responsible":"sender|recipient|domain|unknown","severity":"high|medium|low"}`

func TestParseOutputIgnoresTemplateBlocks(t *testing.T) {
	echo := ReportBegin + "\n" + echoedTemplate + "\n" + ReportEnd + "\n" + MetaBegin + "\n" + templateMeta + "\n" + MetaEnd + "\n"
	// Template only: no report, no META.
	out := ParseOutput("user\n" + echo)
	if out.ReportParsed || out.MetaParsed || out.Report != "" || out.Meta.Summary != "" {
		t.Fatalf("template-only output must yield nothing: %+v", out)
	}
	if err := ValidateOutput(out); err == nil {
		t.Fatal("template-only output must not validate")
	}
	// Template first, real answer later: the real answer wins.
	real := ReportBegin + "\n" + sampleReport + "\n" + ReportEnd + "\n" + MetaBegin + "\n{\"summary\":\"s\",\"responsible\":\"recipient\",\"severity\":\"low\"}\n" + MetaEnd
	out = ParseOutput(echo + "codex\n" + real)
	if !out.ReportParsed || out.Report != sampleReport || !out.MetaParsed || out.Meta.Summary != "s" {
		t.Fatalf("real answer after the template not picked: %+v", out)
	}
	// Real answer first, template echoed afterwards (e.g. a trailing quote):
	// the template must not override the real answer.
	out = ParseOutput(real + "\n" + echo)
	if !out.ReportParsed || out.Report != sampleReport || out.Meta.Summary != "s" {
		t.Fatalf("template after the real answer must be ignored: %+v", out)
	}
	// Template META with a real report: summary derived from the report.
	out = ParseOutput(ReportBegin + "\n" + sampleReport + "\n" + ReportEnd + "\n" + MetaBegin + "\n" + templateMeta + "\n" + MetaEnd)
	if out.MetaParsed || out.Meta.Summary != sampleFirstParagraph {
		t.Fatalf("template META must be dropped and the summary derived: %+v", out)
	}
}

func TestStripPromptEcho(t *testing.T) {
	prompt := "=== CONSTRAINTS ===\n- rule\n\n=== OUTPUT ===\n" + ReportBegin + "\n<what failed and why, x>\n" + ReportEnd + "\nReminder: output nothing after the last marker.\n"
	answer := "codex\n" + ReportBegin + "\n" + sampleReport + "\n" + ReportEnd + "\n"
	cases := []struct {
		name, raw, want string
	}{
		{"verbatim", "header\nuser\n" + prompt + answer, "\n" + answer},
		{"crlf", strings.ReplaceAll("header\r\nuser\r\n"+prompt+answer, "\n", "\r\n"), "\n" + answer},
		{"reformatted echo", "user\n=== CONSTRAINTS ===\n  - rule (indented by the CLI)\nReminder: output nothing after the last marker.\n" + answer, "\n" + answer},
		{"no echo", answer, answer},
		{"empty prompt", answer, answer},
		{"first line only", "=== CONSTRAINTS ===\n" + answer, "=== CONSTRAINTS ===\n" + answer},
	}
	for _, c := range cases {
		p := prompt
		if c.name == "empty prompt" {
			p = ""
		}
		if got := StripPromptEcho(c.raw, p); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	out := ParseTranscript("user\n"+prompt+"\nERROR: You've hit your usage limit. try again at 7:22 PM.\n", prompt)
	if out.ReportParsed {
		t.Fatal("the echoed template must not be parsed as a report")
	}
	if out := ParseTranscript("user\n"+prompt+answer, prompt); !out.ReportParsed || out.Report != sampleReport {
		t.Fatalf("the answer after the echo must be parsed: %+v", out)
	}
}

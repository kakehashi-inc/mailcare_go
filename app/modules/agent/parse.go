package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MinReportRunes is the smallest report body (runes, headings and blank lines
// removed) accepted as a real analysis. Anything shorter is treated as a
// failed run (a truncated or refused answer) so that it never replaces a good
// earlier report.
const MinReportRunes = 40

// placeholderLine matches a line that is nothing but a prompt template
// placeholder such as "<what failed and why, citing ...>" or
// "1. <concrete action>", optionally prefixed by a list number. The bracket
// content must contain a space so that a lone token like "<addr>" or an
// address in angle brackets quoted from a notice does not count.
var placeholderLine = regexp.MustCompile(`(?m)^\s*(?:\d+[.)]\s*)?<[^<>\n]*\s[^<>\n]*>\s*$`)

// placeholderSummary matches a META summary that is still the template
// placeholder ("<one or two sentences>" or any bracketed phrase).
var placeholderSummary = regexp.MustCompile(`^<[^<>]*>$`)

// secretLikePatterns match strings that look like the secrets MailCare or its
// host may hold: the master key (64 hex digits), a bcrypt password hash, a
// PEM block, an API token (mlc_ + 40 hex digits) and an AWS access key id. A
// report or summary that carries one is refused (ErrSecretLikeContent): the
// agent could only have obtained it by reading outside the mail files, or by
// following an instruction planted in a notice.
var secretLikePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`),
	regexp.MustCompile(`\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{20,}`),
	regexp.MustCompile(`-----BEGIN`),
	regexp.MustCompile(`\bmlc_[0-9a-fA-F]{40}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
}

// ErrSecretLikeContent is the ValidateOutput failure for a report or summary
// that contains a secret-like string (see secretLikePatterns).
var ErrSecretLikeContent = errors.New("report contains secret-like content")

// ContainsSecretLike reports whether s matches one of secretLikePatterns.
func ContainsSecretLike(s string) bool {
	for _, re := range secretLikePatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// Meta is the machine-readable part of the agent output (the META block).
type Meta struct {
	Summary     string `json:"summary"`
	Responsible string `json:"responsible"` // sender | recipient | domain | unknown | ""
	Severity    string `json:"severity"`    // high | medium | low | ""
}

// Output is the parsed agent transcript.
type Output struct {
	Report       string // Markdown between the last REPORT marker pair ("" when absent)
	ReportParsed bool   // false when no complete REPORT block was found
	Meta         Meta   // validated META values (invalid enums become "")
	MetaParsed   bool   // false when the META block was absent or not valid JSON
}

// StripPromptEcho removes everything up to and including the first echo of
// prompt from raw so that the markers and placeholders inside the prompt are
// never mistaken for the answer. Line endings are normalized to LF first.
// When the prompt is not echoed verbatim, the span from the first occurrence
// of its first line to the following occurrence of its last line is removed
// instead; when neither is found raw is returned unchanged.
func StripPromptEcho(raw, prompt string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	prompt = strings.TrimSpace(strings.ReplaceAll(prompt, "\r\n", "\n"))
	if prompt == "" {
		return raw
	}
	if i := strings.Index(raw, prompt); i >= 0 {
		return raw[i+len(prompt):]
	}
	var lines []string
	for _, l := range strings.Split(prompt, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			lines = append(lines, t)
		}
	}
	if len(lines) < 2 {
		return raw
	}
	first, last := lines[0], lines[len(lines)-1]
	fi := strings.Index(raw, first)
	if fi < 0 {
		return raw
	}
	li := strings.Index(raw[fi:], last)
	if li < 0 {
		return raw
	}
	return raw[fi+li+len(last):]
}

// ParseOutput extracts the REPORT and META blocks from a CLI transcript. The
// last complete REPORT marker pair that is not merely the prompt template
// (every prose line a placeholder) wins; template-only pairs are ignored, so
// an echoed prompt never counts as a report. META is parsed tolerantly:
// surrounding whitespace and code fences are ignored, a block that is still
// not a JSON object or that only repeats the template placeholder is dropped
// without failing the report. When META carries no summary, one is derived
// from the first non-heading paragraph of the report.
func ParseOutput(raw string) Output {
	var out Output
	for _, block := range extractBlocks(raw, ReportBegin, ReportEnd) {
		if !isTemplateReport(block) {
			out.Report, out.ReportParsed = block, true
		}
	}
	for _, block := range extractBlocks(raw, MetaBegin, MetaEnd) {
		if m, ok := parseMeta(block); ok && !placeholderSummary.MatchString(m.Summary) {
			out.Meta, out.MetaParsed = m, true
		}
	}
	if out.Meta.Summary == "" {
		out.Meta.Summary = deriveSummary(out.Report)
	}
	return out
}

// isTemplateReport reports whether every prose line of a REPORT block is a
// placeholder (the OUTPUT template of the prompt, echoed back).
func isTemplateReport(block string) bool {
	prose := 0
	for _, line := range strings.Split(block, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if !placeholderLine.MatchString(t) {
			return false
		}
		prose++
	}
	return prose > 0
}

// ValidateOutput reports why a parsed output that has a REPORT block is not
// a usable analysis: the report still carries template placeholders (the
// agent echoed the prompt), the report body is shorter than MinReportRunes,
// the META summary is a placeholder, or the report or summary contains a
// secret-like string (ErrSecretLikeContent). It returns nil for a usable
// output. Callers treat a non-nil error as a failed run so that the previous
// good report is kept.
func ValidateOutput(out Output) error {
	if !out.ReportParsed {
		return errors.New("no report block")
	}
	if m := placeholderLine.FindString(out.Report); m != "" {
		return fmt.Errorf("report still contains the template placeholder %q", strings.TrimSpace(m))
	}
	if n := utf8.RuneCountInString(reportBody(out.Report)); n < MinReportRunes {
		return fmt.Errorf("report body too short (%d runes, minimum %d)", n, MinReportRunes)
	}
	if placeholderSummary.MatchString(strings.TrimSpace(out.Meta.Summary)) {
		return fmt.Errorf("summary is the template placeholder %q", out.Meta.Summary)
	}
	if ContainsSecretLike(out.Report) || ContainsSecretLike(out.Meta.Summary) {
		return ErrSecretLikeContent
	}
	return nil
}

// reportBody returns the report without heading lines, blank lines and
// surrounding whitespace, joined by single spaces.
func reportBody(report string) string {
	var parts []string
	for _, line := range strings.Split(report, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, " ")
}

// extractBlocks returns the trimmed text of every complete begin/end marker
// pair in order of appearance. A begin marker without a following end marker
// ends the scan.
func extractBlocks(s, begin, end string) []string {
	var blocks []string
	for {
		bi := strings.Index(s, begin)
		if bi < 0 {
			return blocks
		}
		rest := s[bi+len(begin):]
		ei := strings.Index(rest, end)
		if ei < 0 {
			return blocks
		}
		blocks = append(blocks, strings.TrimSpace(rest[:ei]))
		s = rest[ei+len(end):]
	}
}

// parseMeta decodes the META block. Code fences and any text outside the
// outermost braces are ignored. Enum fields that hold an unexpected value are
// cleared rather than rejected.
func parseMeta(text string) (Meta, bool) {
	text = strings.TrimSpace(text)
	if i := strings.Index(text, "{"); i >= 0 {
		text = text[i:]
	}
	if i := strings.LastIndex(text, "}"); i >= 0 {
		text = text[:i+1]
	}
	var m Meta
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		return Meta{}, false
	}
	m.Summary = collapseSpace(m.Summary)
	m.Responsible = validEnum(m.Responsible, ResponsibleSender, ResponsibleRecipient, ResponsibleDomain, ResponsibleUnknown)
	m.Severity = validEnum(m.Severity, SeverityHigh, SeverityMedium, SeverityLow)
	return m, true
}

// validEnum returns the normalized value when it is one of allowed, else "".
func validEnum(v string, allowed ...string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, a := range allowed {
		if v == a {
			return a
		}
	}
	return ""
}

// collapseSpace trims s and folds every run of whitespace (including line
// breaks) into one space so the value fits a single-line summary.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// deriveSummary returns the first paragraph of the report that is not a
// heading, folded onto one line and cut to MaxSummaryRunes runes.
func deriveSummary(report string) string {
	var lines []string
	flush := func() string { return collapseSpace(strings.Join(lines, " ")) }
	for _, line := range strings.Split(report, "\n") {
		t := strings.TrimSpace(line)
		isHeading := strings.HasPrefix(t, "#")
		if t == "" || isHeading {
			if len(lines) > 0 {
				return truncateRunes(flush(), MaxSummaryRunes)
			}
			continue // blank or heading before any prose: keep looking
		}
		lines = append(lines, t)
	}
	if len(lines) == 0 {
		return ""
	}
	return truncateRunes(flush(), MaxSummaryRunes)
}

// truncateRunes cuts s to at most n runes.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

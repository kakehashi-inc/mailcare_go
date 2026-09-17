package agent

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

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

// ParseOutput extracts the REPORT and META blocks from the raw CLI transcript.
// The last complete REPORT marker pair wins (agents sometimes echo the prompt,
// which contains the markers, before the real answer). META is parsed
// tolerantly: surrounding whitespace and code fences are ignored and a block
// that is still not a JSON object is dropped without failing the report. When
// META carries no summary, one is derived from the first non-heading
// paragraph of the report.
func ParseOutput(raw string) Output {
	var out Output
	out.Report, out.ReportParsed = extractBlock(raw, ReportBegin, ReportEnd)
	if metaText, ok := extractBlock(raw, MetaBegin, MetaEnd); ok {
		out.Meta, out.MetaParsed = parseMeta(metaText)
	}
	if out.Meta.Summary == "" {
		out.Meta.Summary = deriveSummary(out.Report)
	}
	return out
}

// extractBlock returns the trimmed text between the last begin marker and the
// following end marker.
func extractBlock(s, begin, end string) (string, bool) {
	bi := strings.LastIndex(s, begin)
	if bi < 0 {
		return "", false
	}
	rest := s[bi+len(begin):]
	ei := strings.Index(rest, end)
	if ei < 0 {
		return "", false
	}
	return strings.TrimSpace(rest[:ei]), true
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

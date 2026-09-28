package mailengine

import (
	"strconv"
	"strings"
)

// Where the diagnostic of a bounce was found (bounces.diagnostic_source):
// DiagnosticSourceDSN for the delivery-status part, a section reference
// ("text:2", "html:1") for a body section, "" when the notice carried no
// diagnostic. The section reference uses the numbering of the section files
// (<key>-2.txt is "text:2").
const DiagnosticSourceDSN = "dsn"

// SectionRef renders a section reference ("text:2").
func SectionRef(kind string, n int) string {
	return kind + ":" + strconv.Itoa(n)
}

// ParseSectionRef splits a section reference into its kind ("text" or
// "html") and its 1-based number; ok is false for anything else (including
// DiagnosticSourceDSN and "").
func ParseSectionRef(ref string) (kind string, n int, ok bool) {
	kind, num, found := strings.Cut(ref, ":")
	if !found || (kind != bodySourceText && kind != bodySourceHTML) {
		return "", 0, false
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 1 {
		return "", 0, false
	}
	return kind, n, true
}

// SourceKind reduces a diagnostic source to its kind: DiagnosticSourceDSN,
// "text", "html" or "".
func SourceKind(source string) string {
	if kind, _, ok := ParseSectionRef(source); ok {
		return kind
	}
	if source == DiagnosticSourceDSN {
		return DiagnosticSourceDSN
	}
	return ""
}

// SectionText returns the n-th (1-based) body section of the given kind as
// plain text: a text section as it is, an HTML section rendered as text the
// way the classifier and the extractor see it (htmlToText). ok is false when
// the message has no such section.
func (pm *ParsedMessage) SectionText(kind string, n int) (string, bool) {
	var sections []string
	switch kind {
	case bodySourceText:
		sections = pm.TextSections
	case bodySourceHTML:
		sections = pm.HTMLSections
	default:
		return "", false
	}
	if n < 1 || n > len(sections) {
		return "", false
	}
	if kind == bodySourceHTML {
		return htmlToText(sections[n-1]), true
	}
	return sections[n-1], true
}

// SectionCount returns how many body sections of the given kind the message
// has.
func (pm *ParsedMessage) SectionCount(kind string) int {
	switch kind {
	case bodySourceText:
		return len(pm.TextSections)
	case bodySourceHTML:
		return len(pm.HTMLSections)
	}
	return 0
}

// locateDiagnostic names where the extractor took the diagnostic from: the
// delivery-status part, or the body section (of the kind read at the time)
// whose unfolded text contains the matched diagnostic. The body the
// extractor reads joins every section of a kind, so the section is found by
// searching each one; when none contains it verbatim (the match spanned a
// section boundary) the comparison is repeated with the whitespace folded,
// and the first section of the kind is named as a last resort.
func locateDiagnostic(pm *ParsedMessage, source, raw string) string {
	switch source {
	case "":
		return ""
	case DiagnosticSourceDSN:
		return DiagnosticSourceDSN
	}
	count := pm.SectionCount(source)
	if count == 0 {
		return ""
	}
	raw = strings.TrimSpace(raw)
	folded := foldSpace(raw)
	for pass := 0; pass < 2; pass++ {
		for n := 1; n <= count; n++ {
			text, _ := pm.SectionText(source, n)
			body := unfold(text)
			if pass == 0 && strings.Contains(body, raw) {
				return SectionRef(source, n)
			}
			if pass == 1 && folded != "" && strings.Contains(foldSpace(body), folded) {
				return SectionRef(source, n)
			}
		}
	}
	return SectionRef(source, 1)
}

// foldSpace folds every run of whitespace (line breaks included) into one
// space and trims the result.
func foldSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

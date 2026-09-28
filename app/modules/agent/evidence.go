package agent

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

// Evidence.
//
// The agent does not read the mail files. MailCare hands it the evidence the
// classification was based on, prepared from the index and the raw notices:
//
//   - the patterns of the group (bounces sharing status code, diagnostic
//     template, remote MTA and kind of diagnostic source, see
//     mailengine.PatternKey) with their counts, so that nothing needs to be
//     read to learn that the other notices look the same;
//   - one sample notice per pattern (the newest, at most MaxEvidenceSamples),
//     each with the delivery-status fields, the headers of the returned
//     message that matter for the analysis, and an excerpt of the body
//     section the diagnostic was found in (bounces.diagnostic_source). The
//     text and HTML bodies of a notice can differ; only the section that
//     holds the diagnostic is shown, the other body is named as not used.
//
// The sample blocks go into the prompt; the same evidence, untruncated,
// goes into evidence/<message_key>.txt in the workspace, which the agent may
// read only under the conditions the prompt names (a [TRUNCATED] excerpt, a
// value marked (not found), a contradiction with the category). Every value
// taken from a notice is folded onto one line, and every body line is
// prefixed with "| ", so that text from a notice can never open a line or a
// section of the prompt.

// Pattern is one bounce pattern of a group.
type Pattern struct {
	ID                 string // "P1", "P2", ... in list order
	Key                string // bounces.pattern_key
	StatusCode         string
	DiagnosticTemplate string
	RemoteMTA          string
	SourceKind         string // dsn | text | html | ""
	Messages           int
	Recipients         int
	FirstSeen          time.Time
	LastSeen           time.Time
	// New is true in an update (EvidenceInput.Covered set) for a pattern the
	// previous report did not cover.
	New    bool
	Sample *Sample // nil when the pattern got no sample

	newest     *models.GroupBounce
	recipients map[string]bool
}

// Sample is the evidence of one sample notice.
type Sample struct {
	ID         string // "S1", "S2", ...
	Pattern    *Pattern
	MessageKey string
	// FileName is the evidence file relative to the workspace
	// ("evidence/<key>.txt", slash-separated).
	FileName string
	Prompt   string // the block for the EVIDENCE section of the prompt
	File     string // the content of the evidence file
}

// Evidence is what BuildEvidence prepares for one analysis run.
type Evidence struct {
	Patterns []*Pattern // most frequent first
	Samples  []*Sample  // in pattern order
	Messages int        // bounces of the group
	// Update is true when the run updates a previous report: some patterns
	// are covered by it, the others (New) appeared since, and only those
	// get samples.
	Update bool
}

// EvidenceInput is everything BuildEvidence needs.
type EvidenceInput struct {
	MailsRoot string
	Address   string
	Group     *models.BounceGroup
	Bounces   []*models.GroupBounce // every bounce of the group, newest first
	// Covered holds the pattern keys the previous completed report covered;
	// nil (or covering none or all of the patterns) means a full analysis.
	Covered map[string]bool
}

// readRawMessage reads the original notice of a message key. A variable so
// that tests can supply their own notices.
var readRawMessage = func(mailsRoot, address, key string) ([]byte, error) {
	return os.ReadFile(mailengine.MessageFilePath(mailsRoot, address, key, "eml"))
}

// BuildEvidence groups the bounces into patterns and prepares the sample
// notices.
func BuildEvidence(in EvidenceInput) *Evidence {
	ev := &Evidence{Messages: len(in.Bounces)}
	byKey := map[string]*Pattern{}
	for _, b := range in.Bounces {
		p := byKey[b.PatternKey]
		if p == nil {
			p = &Pattern{
				Key: b.PatternKey, StatusCode: b.StatusCode, DiagnosticTemplate: b.DiagnosticTemplate,
				RemoteMTA: b.RemoteMTA, SourceKind: mailengine.SourceKind(b.DiagnosticSource),
				FirstSeen: b.Date, LastSeen: b.Date, newest: b, recipients: map[string]bool{},
			}
			byKey[b.PatternKey] = p
			ev.Patterns = append(ev.Patterns, p)
		}
		p.Messages++
		if b.Recipient != "" {
			p.recipients[strings.ToLower(b.Recipient)] = true
		}
		if b.Date.Before(p.FirstSeen) {
			p.FirstSeen = b.Date
		}
		if b.Date.After(p.LastSeen) {
			p.LastSeen = b.Date
		}
	}
	sort.SliceStable(ev.Patterns, func(i, j int) bool {
		if ev.Patterns[i].Messages != ev.Patterns[j].Messages {
			return ev.Patterns[i].Messages > ev.Patterns[j].Messages
		}
		return ev.Patterns[i].LastSeen.After(ev.Patterns[j].LastSeen)
	})
	newCount := 0
	for i, p := range ev.Patterns {
		p.ID = fmt.Sprintf("P%d", i+1)
		p.Recipients = len(p.recipients)
		if in.Covered != nil && !in.Covered[p.Key] {
			p.New = true
			newCount++
		}
	}
	ev.Update = in.Covered != nil && newCount > 0 && newCount < len(ev.Patterns)
	if !ev.Update {
		for _, p := range ev.Patterns {
			p.New = false
		}
	}
	for _, p := range ev.Patterns {
		if len(ev.Samples) >= MaxEvidenceSamples {
			break
		}
		if ev.Update && !p.New {
			continue
		}
		s := &Sample{
			ID: fmt.Sprintf("S%d", len(ev.Samples)+1), Pattern: p, MessageKey: p.newest.MessageKey,
			FileName: EvidenceDirName + "/" + p.newest.MessageKey + ".txt",
		}
		buildSample(s, in, p.newest)
		p.Sample = s
		ev.Samples = append(ev.Samples, s)
	}
	return ev
}

// PatternKeys returns the keys of every pattern (what a completed report
// covers).
func (ev *Evidence) PatternKeys() []string {
	keys := make([]string, 0, len(ev.Patterns))
	for _, p := range ev.Patterns {
		keys = append(keys, p.Key)
	}
	sort.Strings(keys)
	return keys
}

// buildSample reads the notice of a sample and renders its prompt block and
// its evidence file.
func buildSample(s *Sample, in EvidenceInput, b *models.GroupBounce) {
	var head strings.Builder
	head.WriteString(fmt.Sprintf("[%s] pattern %s, message %s, %s\n", s.ID, s.Pattern.ID, b.MessageKey, formatTime(models.NullTime(b.Date))))
	head.WriteString("Classification: category " + orPlaceholder(foldLine(in.Group.Category), "(not classified)") +
		", rule " + orPlaceholder(foldLine(b.CategoryRule), "(not recorded)") + "\n")
	head.WriteString("Diagnostic: " + orNotFound(sanitize(foldLine(b.Diagnostic))) + "\n")
	head.WriteString("Diagnostic source: " + describeSource(b.DiagnosticSource) + "\n")

	raw, err := readRawMessage(in.MailsRoot, in.Address, b.MessageKey)
	if err != nil {
		note := "Notice: (not found: the notice file could not be read)\n"
		s.Prompt = head.String() + note
		s.File = head.String() + note
		return
	}
	pm := mailengine.ParseMessage(raw)
	var prompt, file strings.Builder
	prompt.WriteString(head.String())
	file.WriteString(head.String())
	notice := "Notice: From: " + orNotFound(sanitize(foldLine(formatFrom(pm)))) + "; Subject: " + orNotFound(sanitize(foldLine(pm.Subject))) + "\n"
	prompt.WriteString(notice)
	file.WriteString(notice)

	dsnLine, dsnFull := deliveryStatusEvidence(pm.DeliveryStatus, b.Recipient)
	prompt.WriteString(dsnLine)
	file.WriteString(dsnFull)
	returned := returnedMessageEvidence(pm.OriginalMessage)
	prompt.WriteString(returned)
	file.WriteString(returned)

	kind, n, note := bodyForSample(pm, b)
	if kind == "" {
		prompt.WriteString(note)
		file.WriteString(note)
		s.Prompt, s.File = prompt.String(), file.String()
		return
	}
	prompt.WriteString(note)
	file.WriteString(note)
	text, _ := pm.SectionText(kind, n)
	lines := cleanBodyLines(text)
	ref := mailengine.SectionRef(kind, n)
	if len(lines) == 0 {
		empty := fmt.Sprintf("Excerpt (%s): (empty)\n", ref)
		prompt.WriteString(empty)
		file.WriteString(empty)
		s.Prompt, s.File = prompt.String(), file.String()
		return
	}
	// When the diagnostic comes from the delivery-status part the body only
	// supports it, so a shorter excerpt is enough.
	maxLines := MaxExcerptLines
	if b.DiagnosticSource == mailengine.DiagnosticSourceDSN {
		maxLines = MaxExcerptLines / 2
	}
	from, to := excerptWindow(lines, anchorLine(lines, b), maxLines)
	prompt.WriteString(fmt.Sprintf("Excerpt (%s, lines %d-%d of %d):\n", ref, from+1, to, len(lines)))
	if from > 0 {
		prompt.WriteString(fmt.Sprintf("[TRUNCATED: %d lines before this excerpt are in %s]\n", from, s.FileName))
	}
	for _, l := range lines[from:to] {
		prompt.WriteString("| " + truncateLine(l, maxExcerptLineRunes) + "\n")
	}
	if to < len(lines) {
		prompt.WriteString(fmt.Sprintf("[TRUNCATED: continues in %s (%d more lines)]\n", s.FileName, len(lines)-to))
	}
	file.WriteString(fmt.Sprintf("Body %s (complete, %d lines):\n", ref, len(lines)))
	shown := lines
	if len(shown) > maxEvidenceFileLines {
		shown = shown[:maxEvidenceFileLines]
	}
	for _, l := range shown {
		file.WriteString("| " + truncateLine(l, maxEvidenceLineRunes) + "\n")
	}
	if len(shown) < len(lines) {
		file.WriteString(fmt.Sprintf("[TRUNCATED: %d more lines not included]\n", len(lines)-len(shown)))
	}
	s.Prompt, s.File = prompt.String(), file.String()
}

const (
	// maxExcerptLineRunes caps one line of a prompt excerpt.
	maxExcerptLineRunes = 300
	// maxEvidenceFileLines and maxEvidenceLineRunes cap the body of an
	// evidence file.
	maxEvidenceFileLines = 2000
	maxEvidenceLineRunes = 2000
)

// describeSource renders a diagnostic source for the evidence.
func describeSource(source string) string {
	switch {
	case source == mailengine.DiagnosticSourceDSN:
		return "the delivery-status part (Diagnostic-Code)"
	case source == "":
		return "(not found: the notice carries no diagnostic)"
	}
	kind, n, ok := mailengine.ParseSectionRef(source)
	if !ok {
		return "(not recorded)"
	}
	if kind == "html" {
		return fmt.Sprintf("body section %s (HTML, rendered as text)", mailengine.SectionRef(kind, n))
	}
	return "body section " + mailengine.SectionRef(kind, n)
}

// bodyForSample picks the body section shown for a sample and the note that
// explains the choice: the section the diagnostic was found in, or, when the
// diagnostic came from the delivery-status part (or there is none), the
// body the notice was classified on (the section of that kind that mentions
// the status code, else its first section). kind is "" when the notice has
// no body at all.
func bodyForSample(pm *mailengine.ParsedMessage, b *models.GroupBounce) (kind string, n int, note string) {
	other := func(kind string) string {
		o := "html"
		if kind == "html" {
			o = "text"
		}
		if pm.SectionCount(o) == 0 {
			return ""
		}
		return fmt.Sprintf(" The %s body was not used for the classification and may say something else; do not use it.", o)
	}
	if k, num, ok := mailengine.ParseSectionRef(b.DiagnosticSource); ok && pm.SectionCount(k) >= num {
		return k, num, fmt.Sprintf("Body used: %s, the section that holds the diagnostic.%s\n", mailengine.SectionRef(k, num), other(k))
	}
	kind = b.BodySource
	if kind != "text" && kind != "html" || pm.SectionCount(kind) == 0 {
		switch {
		case pm.SectionCount("text") > 0:
			kind = "text"
		case pm.SectionCount("html") > 0:
			kind = "html"
		default:
			return "", 0, "Body used: (not found: the notice has no text or HTML body)\n"
		}
	}
	n = 1
	if b.StatusCode != "" {
		for i := 1; i <= pm.SectionCount(kind); i++ {
			if text, _ := pm.SectionText(kind, i); strings.Contains(text, b.StatusCode) {
				n = i
				break
			}
		}
	}
	return kind, n, fmt.Sprintf("Body used: %s, the body the notice was classified on (the diagnostic itself is not in a body section).%s\n",
		mailengine.SectionRef(kind, n), other(kind))
}

// deliveryStatusEvidence renders the delivery-status part: one line for the
// prompt (the per-recipient block of the bounce's recipient, else the
// first) and every field of every block for the evidence file.
func deliveryStatusEvidence(ds *mailengine.DeliveryStatus, recipient string) (line, full string) {
	if ds == nil {
		none := "Delivery status: (not found: the notice has no delivery-status part)\n"
		return none, none
	}
	var pick *mailengine.DeliveryStatusRecipient
	for i := range ds.Recipients {
		r := &ds.Recipients[i]
		if strings.EqualFold(r.FinalRecipient, recipient) || strings.EqualFold(r.OriginalRecipient, recipient) {
			pick = r
			break
		}
	}
	if pick == nil && len(ds.Recipients) > 0 {
		pick = &ds.Recipients[0]
	}
	fields := []string{"Reporting-MTA: " + orNotFound(sanitize(foldLine(ds.ReportingMTA)))}
	if pick != nil {
		fields = append(fields, recipientFields(pick)...)
	} else {
		fields = append(fields, "per-recipient fields: (not found)")
	}
	line = "Delivery status: " + strings.Join(fields, "; ")
	if len(ds.Recipients) > 1 {
		line += fmt.Sprintf(" (1 of %d recipient blocks)", len(ds.Recipients))
	}
	line += "\n"

	var b strings.Builder
	b.WriteString("Delivery status (every field):\n")
	b.WriteString("  Reporting-MTA: " + orNotFound(sanitize(foldLine(ds.ReportingMTA))) + "\n")
	b.WriteString("  Arrival-Date: " + orNotFound(sanitize(foldLine(ds.ArrivalDate))) + "\n")
	for i := range ds.Recipients {
		b.WriteString(fmt.Sprintf("  Recipient block %d:\n", i+1))
		for _, f := range recipientFields(&ds.Recipients[i]) {
			b.WriteString("    " + f + "\n")
		}
	}
	return line, b.String()
}

func recipientFields(r *mailengine.DeliveryStatusRecipient) []string {
	return []string{
		"Final-Recipient: " + orNotFound(sanitize(foldLine(r.FinalRecipient))),
		"Original-Recipient: " + orNotFound(sanitize(foldLine(r.OriginalRecipient))),
		"Action: " + orNotFound(foldLine(r.Action)),
		"Status: " + orNotFound(foldLine(r.Status)),
		"Remote-MTA: " + orNotFound(sanitize(foldLine(r.RemoteMTA))),
		"Diagnostic-Code: " + orNotFound(sanitize(foldLine(r.DiagnosticCode))),
	}
}

// returnedMessageEvidence renders the headers of the returned original
// message that matter for the analysis (the sender authentication results
// included).
func returnedMessageEvidence(om *mailengine.OriginalMessage) string {
	if om == nil {
		return "Returned message: (not found: the notice includes neither the original message nor its headers)\n"
	}
	date := ""
	if !om.Date.IsZero() {
		date = formatTime(models.NullTime(om.Date))
	}
	fields := []string{
		"From: " + orNotFound(sanitize(foldLine(om.From))),
		"To: " + orNotFound(sanitize(foldLine(om.To))),
		"Subject: " + orNotFound(sanitize(foldLine(om.Subject))),
		"Date: " + orNotFound(date),
		"Message-ID: " + orNotFound(sanitize(foldLine(om.MessageID))),
		"DKIM-Signature d=: " + orNotFound(sanitize(foldLine(strings.Join(om.DKIMDomains, ", ")))),
		"Authentication-Results: " + orNotFound(sanitize(foldLine(strings.Join(om.AuthenticationResults, " / ")))),
		"Received-SPF: " + orNotFound(sanitize(foldLine(strings.Join(om.ReceivedSPF, " / ")))),
	}
	return "Returned message: " + strings.Join(fields, "; ") + "\n"
}

// formatFrom renders the sender of a notice.
func formatFrom(pm *mailengine.ParsedMessage) string {
	if pm.FromName != "" && pm.FromAddress != "" {
		return pm.FromName + " <" + pm.FromAddress + ">"
	}
	return pm.FromAddress
}

// cleanBodyLines splits a body into lines with trailing spaces removed,
// runs of blank lines folded into one and leading / trailing blank lines
// dropped, and neutralizes the output markers (sanitize).
func cleanBodyLines(text string) []string {
	var out []string
	blank := false
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		l = strings.TrimRight(l, " \t\r")
		if strings.TrimSpace(l) == "" {
			blank = len(out) > 0
			continue
		}
		if blank {
			out = append(out, "")
			blank = false
		}
		out = append(out, sanitize(l))
	}
	return out
}

// anchorLine returns the index of the line the excerpt is centered on: the
// line holding the start of the diagnostic, else the status code, else the
// recipient, else the first line.
func anchorLine(lines []string, b *models.GroupBounce) int {
	var needles []string
	if d := foldLine(b.Diagnostic); d != "" {
		r := []rune(d)
		if len(r) > 40 {
			r = r[:40]
		}
		needles = append(needles, string(r))
	}
	for _, v := range []string{b.StatusCode, b.Recipient} {
		if v != "" {
			needles = append(needles, v)
		}
	}
	for _, needle := range needles {
		for i, l := range lines {
			if strings.Contains(strings.ToLower(foldLine(l)), strings.ToLower(needle)) {
				return i
			}
		}
	}
	return 0
}

// excerptWindow returns the [from, to) range of at most max lines around
// anchor (a quarter of the window before it, the rest after it).
func excerptWindow(lines []string, anchor, max int) (from, to int) {
	if len(lines) <= max {
		return 0, len(lines)
	}
	from = anchor - max/4
	if from < 0 {
		from = 0
	}
	to = from + max
	if to > len(lines) {
		to = len(lines)
		from = to - max
	}
	return from, to
}

// truncateLine cuts a line to n runes, marking the cut.
func truncateLine(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return truncateRunes(s, n) + " ..."
}

// sanitize neutralizes the output markers inside text taken from a notice,
// so that a notice can never supply a REPORT or META block.
func sanitize(s string) string {
	return strings.ReplaceAll(s, "<<<MLC", "<<< MLC")
}

func orNotFound(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(not found)"
	}
	return s
}

func orPlaceholder(s, placeholder string) string {
	if strings.TrimSpace(s) == "" {
		return placeholder
	}
	return s
}

package agent

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// classification was based on, prepared from the index and the raw notices
// (for a DMARC group: from the failing records of the aggregate reports, a
// record per sample instead of a notice body):
//
//   - the patterns of the group (bounces sharing status code, diagnostic
//     template, remote MTA and kind of diagnostic source, see
//     mailengine.PatternKey) with their counts, so that nothing needs to be
//     read to learn that the other notices look the same;
//   - one sample notice per pattern (the newest), each with the
//     delivery-status fields, the headers of the returned message that
//     matter for the analysis, and an excerpt of the body section the
//     diagnostic was found in (bounces.diagnostic_source). The text and
//     HTML bodies of a notice can differ; only the section that holds the
//     diagnostic is shown, the other body is named as not used.
//
// The members are counted per notice (a notice that reports several failed
// recipients has a bounces row per recipient: it counts once among the
// messages and with each of its recipients) and per record of a DMARC
// report. Every pattern gets its sample; the blocks of the first
// MaxEvidenceSamples (Sample.Inline) go into the prompt, which names the
// evidence file of every other one. The evidence of each sample goes into
// evidence/<message_key>.txt in the workspace
// (evidence/<message_key>-r<n>.txt for the n-th further failed recipient
// of a notice; for a DMARC record the same capped block as in the prompt,
// in evidence/<message_key>-dmarc-<record id>.txt, see evidenceFileName):
// the block of the prompt, with every DNS evidence line. What a notice
// holds as a whole (every delivery-status block, the whole body sections
// its samples use) goes once into evidence/<message_key>-notice.txt,
// which the sample files name, so a sample file stays as small as its
// block however many recipients the notice reports. The files are written
// as the samples are made (EvidenceInput.Dir), and every notice is read
// and parsed once. The agent may read them only under the conditions the
// prompt names (a [TRUNCATED] excerpt, a value marked (not found), a
// contradiction with the category, a sample not shown in the prompt). Every value
// taken from a notice is folded onto one line, and every body line is
// prefixed with "| ", so that text from a notice can never open a line or a
// section of the prompt.
//
// A sample whose diagnostic reports a failed DNS lookup (design document
// "mail classification" 4.4) also carries what was parsed from it and the
// verification MailCare stored for the name (EvidenceInput.DNSCheck, see
// dnsFailureEvidence); the stored evidence lines come from DNS responses and
// are quoted like body lines.

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
	// Before and After count the members of the pattern that arrived before
	// and after the decision being re-checked (EvidenceInput.Decided set),
	// SentAfter the members about mail sent after it (mailengine.SentAt);
	// FirstAfter / LastAfter date the members after it.
	Before, After, SentAfter int
	FirstAfter, LastAfter    time.Time
	// SourceIPs are the sending IPs of the DMARC records of the pattern
	// (sorted; empty for bounces).
	SourceIPs []string

	newest      *models.GroupBounce
	newestAfter *models.GroupBounce
	recipients  map[string]bool
	sources     map[string]bool
	// units, before and after hold the notices counted (noticeKey), so
	// that a notice with several failed recipients counts once.
	units, before, after map[string]bool
}

// Sample is the evidence of one sample notice.
type Sample struct {
	ID         string // "S1", "S2", ...
	Pattern    *Pattern
	MessageKey string
	// FileName is the evidence file relative to the workspace
	// ("evidence/<key>.txt", slash-separated; see evidenceFileName).
	FileName string
	// NoticeFile is the file of the whole notice of the sample (every
	// delivery-status block, every body section the samples of the notice
	// use), shared by the samples of the notice
	// ("evidence/<message_key>-notice.txt"); "" for a DMARC record and for
	// a notice that could not be read.
	NoticeFile string
	Prompt     string // the block for the EVIDENCE section of the prompt
	// DNS is true when the block reports a failed DNS lookup (the prompt
	// then explains how to read the verification).
	DNS bool
	// Inline is true for the samples whose block the EVIDENCE section shows
	// (the first MaxEvidenceSamples in sampling order); the others are only
	// in their evidence files.
	Inline bool

	member *models.GroupBounce // the member the sample shows
}

// Evidence is what BuildEvidence prepares for one analysis run.
type Evidence struct {
	Patterns []*Pattern // most frequent first
	Samples  []*Sample  // in sampling order
	Messages int        // members of the group: notices (noticeKey) or DMARC records
	// Update is true when the run updates a previous report: some patterns
	// are covered by it, the others (New) appeared since, and only those
	// get samples.
	Update bool
	// Recheck is true when the run re-checks a decision: the patterns carry
	// their counts before and after it, and the samples come from after it.
	Recheck bool
	// Dir is the workspace the evidence files were written to.
	Dir string
}

// EvidenceInput is everything BuildEvidence needs.
type EvidenceInput struct {
	// Dir is the workspace: the evidence files are written into its
	// evidence directory as the samples are made.
	Dir       string
	MailsRoot string
	Address   string
	Group     *models.BounceGroup
	Bounces   []*models.GroupBounce // every member of the group (bounces, DMARC records), newest first
	// Covered holds the pattern keys the previous completed report covered;
	// nil (or covering none or all of the patterns) means a full analysis.
	Covered map[string]bool
	// Decided is when the decision being re-checked was made (zero when
	// none): the members are then counted before and after it, and the
	// samples are taken from after it (Covered is not used).
	Decided time.Time
	// DNSCheck finds the stored DNS verification of the name a sample's
	// diagnostic reports as a failed lookup; nil = none is available.
	DNSCheck DNSCheckFunc
}

// DNSCheckResult is a verification of a name that MailCare stored after
// looking it up again (a dns_checks row, design document "mail
// classification" 4.4).
type DNSCheckResult struct {
	Outcome   string    // exists | not_found | broken
	Detail    string    // the evidence, one line per resolver and authoritative server
	Direct    bool      // the authoritative servers of the name were asked as well (dns_checks.direct)
	CheckedAt time.Time // when the name was looked up
}

// DNSCheckFunc returns the stored verification a notice received at
// noticeTime uses for a name (the one mailengine.ParseDNSFailure reports,
// or the recipient domain for a form that names none; the record type does
// not matter); ok = false when there is none. The caller supplies it
// because the verifications live in the master database, which this
// package does not open.
type DNSCheckFunc func(name string, noticeTime time.Time) (res DNSCheckResult, ok bool)

// readRawMessage reads the original notice of a message key. A variable so
// that tests can supply the notices.
var readRawMessage = func(mailsRoot, address, key string) ([]byte, error) {
	return os.ReadFile(mailengine.MessageFilePath(mailsRoot, address, key, "eml"))
}

// BuildEvidence groups the bounces into patterns, prepares the sample
// notices and writes their evidence files into the workspace (in.Dir). An
// error is one of writing the files.
func BuildEvidence(in EvidenceInput) (*Evidence, error) {
	ev := &Evidence{Dir: in.Dir}
	byKey := map[string]*Pattern{}
	units := map[string]bool{}
	for _, b := range in.Bounces {
		unit := noticeKey(b)
		if countOnce(units, unit) {
			ev.Messages++
		}
		p := byKey[b.PatternKey]
		if p == nil {
			p = &Pattern{
				Key: b.PatternKey, StatusCode: b.StatusCode, DiagnosticTemplate: b.DiagnosticTemplate,
				RemoteMTA: b.RemoteMTA, SourceKind: mailengine.SourceKind(b.DiagnosticSource),
				FirstSeen: b.Date, LastSeen: b.Date, newest: b, recipients: map[string]bool{}, sources: map[string]bool{},
				units: map[string]bool{}, before: map[string]bool{}, after: map[string]bool{},
			}
			byKey[b.PatternKey] = p
			ev.Patterns = append(ev.Patterns, p)
		}
		if countOnce(p.units, unit) {
			p.Messages++
		}
		if b.Recipient != "" {
			p.recipients[strings.ToLower(b.Recipient)] = true
		}
		if b.DMARC != nil && b.DMARC.SourceIP != "" {
			p.sources[b.DMARC.SourceIP] = true
		}
		if b.Date.Before(p.FirstSeen) {
			p.FirstSeen = b.Date
		}
		if b.Date.After(p.LastSeen) {
			p.LastSeen = b.Date
		}
		if in.Decided.IsZero() {
			continue
		}
		if !b.Date.After(in.Decided) {
			if countOnce(p.before, unit) {
				p.Before++
			}
			continue
		}
		if countOnce(p.after, unit) {
			p.After++
			if mailengine.SentAt(b).After(in.Decided) {
				p.SentAfter++
			}
		}
		if p.newestAfter == nil {
			// The members come newest first.
			p.newestAfter, p.LastAfter = b, b.Date
		}
		p.FirstAfter = b.Date
	}
	sort.SliceStable(ev.Patterns, func(i, j int) bool {
		if ev.Patterns[i].Messages != ev.Patterns[j].Messages {
			return ev.Patterns[i].Messages > ev.Patterns[j].Messages
		}
		return ev.Patterns[i].LastSeen.After(ev.Patterns[j].LastSeen)
	})
	if !in.Decided.IsZero() {
		ev.Recheck = true
		in.Covered = nil
	}
	newCount := 0
	for i, p := range ev.Patterns {
		p.ID = fmt.Sprintf("P%d", i+1)
		p.Recipients = len(p.recipients)
		for ip := range p.sources {
			p.SourceIPs = append(p.SourceIPs, ip)
		}
		sort.Strings(p.SourceIPs)
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
	for _, p := range samplingOrder(ev) {
		if ev.Update && !p.New {
			continue
		}
		member := p.newest
		if p.newestAfter != nil {
			member = p.newestAfter
		}
		s := &Sample{
			ID: fmt.Sprintf("S%d", len(ev.Samples)+1), Pattern: p, MessageKey: member.MessageKey,
			FileName: evidenceFileName(member), Inline: len(ev.Samples) < MaxEvidenceSamples, member: member,
		}
		p.Sample = s
		ev.Samples = append(ev.Samples, s)
	}
	return ev, writeSamples(in, ev)
}

// writeSamples makes the samples and writes their evidence files: a DMARC
// record alone, the samples of a notice together, so that the notice is
// read and parsed once and its notice file written once.
func writeSamples(in EvidenceInput, ev *Evidence) error {
	if err := os.MkdirAll(filepath.Join(in.Dir, EvidenceDirName), 0o700); err != nil {
		return err
	}
	var keys []string
	byNotice := map[string][]*Sample{}
	for _, s := range ev.Samples {
		if s.member.DMARC != nil {
			if err := writeEvidenceFile(in.Dir, s.FileName, func(w io.Writer) { buildDMARCSample(s, in, s.member, w) }); err != nil {
				return err
			}
			continue
		}
		if byNotice[s.MessageKey] == nil {
			keys = append(keys, s.MessageKey)
		}
		byNotice[s.MessageKey] = append(byNotice[s.MessageKey], s)
	}
	for _, key := range keys {
		if err := writeNotice(in, key, byNotice[key]); err != nil {
			return err
		}
	}
	return nil
}

// writeEvidenceFile creates a file of the workspace (name relative to it,
// slash-separated) and streams its content to it.
func writeEvidenceFile(dir, name string, fill func(w io.Writer)) error {
	f, err := os.OpenFile(filepath.Join(dir, filepath.FromSlash(name)), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	fill(w)
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// noticeFileName is the notice file of a message key (Sample.NoticeFile).
func noticeFileName(key string) string {
	return EvidenceDirName + "/" + key + "-notice.txt"
}

// bodySection names one body section of a notice.
type bodySection struct {
	kind string
	n    int
}

// writeNotice reads and parses a notice once, writes the file of each of
// its samples and then its notice file: every delivery-status block and
// every body section the samples use, whole.
func writeNotice(in EvidenceInput, key string, samples []*Sample) error {
	var pm *mailengine.ParsedMessage
	if raw, err := readRawMessage(in.MailsRoot, in.Address, key); err == nil {
		pm = mailengine.ParseMessage(raw)
	}
	noticeFile := ""
	if pm != nil {
		noticeFile = noticeFileName(key)
	}
	var sections []bodySection
	seen := map[bodySection]bool{}
	for _, s := range samples {
		s.NoticeFile = noticeFile
		var sec bodySection
		if err := writeEvidenceFile(in.Dir, s.FileName, func(w io.Writer) { sec = buildSample(s, in, s.member, pm, w) }); err != nil {
			return err
		}
		if sec.kind != "" && !seen[sec] {
			seen[sec] = true
			sections = append(sections, sec)
		}
	}
	if pm == nil {
		return nil
	}
	return writeEvidenceFile(in.Dir, noticeFile, func(w io.Writer) {
		fmt.Fprintf(w, "Notice %s: every delivery-status block and the body sections its samples use\n", key)
		writeDeliveryStatusBlocks(w, pm.DeliveryStatus)
		for _, sec := range sections {
			text, _ := pm.SectionText(sec.kind, sec.n)
			lines := cleanBodyLines(text)
			fmt.Fprintf(w, "Body %s (complete, %d lines):\n", mailengine.SectionRef(sec.kind, sec.n), len(lines))
			shown := lines
			if len(shown) > maxEvidenceFileLines {
				shown = shown[:maxEvidenceFileLines]
			}
			for _, l := range shown {
				_, _ = io.WriteString(w, "| "+truncateLine(l, maxEvidenceLineRunes)+"\n")
			}
			if len(shown) < len(lines) {
				fmt.Fprintf(w, "[TRUNCATED: %d more lines not included]\n", len(lines)-len(shown))
			}
		}
	})
}

// noticeKey identifies the notice of a bounce among the members (every
// failed recipient of a notice has a separate row, and the notice counts
// once); "" for a DMARC record, which counts alone.
func noticeKey(b *models.GroupBounce) string {
	if b.DMARC != nil {
		return ""
	}
	return b.MessageKey
}

// countOnce reports whether a member of the given notice is to be counted
// (the first of its notice in seen, or a member without one) and records it.
func countOnce(seen map[string]bool, key string) bool {
	if key == "" {
		return true
	}
	if seen[key] {
		return false
	}
	seen[key] = true
	return true
}

// samplingOrder is the order in which the patterns get the samples shown in
// the prompt.
// Normally the list order (most frequent first). When a decision is
// re-checked, what arrived after it comes first: the patterns that appeared
// only after it, then the ones that continued after it (each by their
// newest member after it), then the rest in list order.
func samplingOrder(ev *Evidence) []*Pattern {
	if !ev.Recheck {
		return ev.Patterns
	}
	var appeared, continued, rest []*Pattern
	for _, p := range ev.Patterns {
		switch {
		case p.After > 0 && p.Before == 0:
			appeared = append(appeared, p)
		case p.After > 0:
			continued = append(continued, p)
		default:
			rest = append(rest, p)
		}
	}
	byNewestAfter := func(ps []*Pattern) {
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].LastAfter.After(ps[j].LastAfter) })
	}
	byNewestAfter(appeared)
	byNewestAfter(continued)
	return append(append(appeared, continued...), rest...)
}

// evidenceFileName is the evidence file of a sample, relative to the
// workspace: evidence/<message_key>.txt for a bounce of the first failed
// recipient of a notice, evidence/<message_key>-r<n>.txt for the n-th
// further one (the failed recipients of a notice can belong to different
// patterns of the same group), and evidence/<message_key>-dmarc-<record
// id>.txt for a DMARC record (a report mail can hold samples of several
// patterns of the same group).
func evidenceFileName(b *models.GroupBounce) string {
	if b.DMARC != nil {
		return fmt.Sprintf("%s/%s-dmarc-%d.txt", EvidenceDirName, b.MessageKey, b.DMARC.ID)
	}
	if b.RecipientIndex > 0 {
		return fmt.Sprintf("%s/%s-r%d.txt", EvidenceDirName, b.MessageKey, b.RecipientIndex)
	}
	return EvidenceDirName + "/" + b.MessageKey + ".txt"
}

// inlineSamples returns the samples whose block the EVIDENCE section shows.
func (ev *Evidence) inlineSamples() []*Sample {
	var out []*Sample
	for _, s := range ev.Samples {
		if s.Inline {
			out = append(out, s)
		}
	}
	return out
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

// hasDNS reports whether any sample reports a failed DNS lookup.
func (ev *Evidence) hasDNS() bool {
	for _, s := range ev.Samples {
		if s.DNS {
			return true
		}
	}
	return false
}

// buildSample renders the prompt block of a sample (s.Prompt) and writes
// its evidence file to w: the same block, with every DNS evidence line, and
// the names of what the notice file holds (pm nil: the notice could not be
// read). It returns the body section the sample uses (kind "" for none).
func buildSample(s *Sample, in EvidenceInput, b *models.GroupBounce, pm *mailengine.ParsedMessage, w io.Writer) bodySection {
	var prompt strings.Builder
	both := func(text string) {
		prompt.WriteString(text)
		_, _ = io.WriteString(w, text)
	}
	defer func() { s.Prompt = prompt.String() }()
	both(fmt.Sprintf("[%s] pattern %s, message %s, %s\n", s.ID, s.Pattern.ID, b.MessageKey, formatTime(models.NullTime(b.Date))))
	both("Classification: category " + orPlaceholder(foldLine(in.Group.Category), "(not classified)") +
		", rule " + orPlaceholder(foldLine(b.CategoryRule), "(not recorded)") + "\n")
	both("Diagnostic: " + orNotFound(sanitize(foldLine(b.Diagnostic))) + "\n")
	both("Diagnostic source: " + describeSource(b.DiagnosticSource) + "\n")
	// The DNS block comes from the index, so it is shown even when the
	// notice file cannot be read.
	dnsPrompt, dnsFile := dnsFailureEvidence(in.DNSCheck, b, s.FileName)
	s.DNS = dnsPrompt != ""
	prompt.WriteString(dnsPrompt)
	_, _ = io.WriteString(w, dnsFile)

	if pm == nil {
		both("Notice: (not found: the notice file could not be read)\n")
		return bodySection{}
	}
	both("Notice: From: " + orNotFound(sanitize(foldLine(formatFrom(pm)))) + "; Subject: " + orNotFound(sanitize(foldLine(pm.Subject))) + "\n")
	both(deliveryStatusEvidence(pm.DeliveryStatus, b.Recipient, s.NoticeFile))
	both(returnedMessageEvidence(pm.OriginalMessage))

	kind, n, note := bodyForSample(pm, b)
	both(note)
	if kind == "" {
		return bodySection{}
	}
	text, _ := pm.SectionText(kind, n)
	lines := cleanBodyLines(text)
	ref := mailengine.SectionRef(kind, n)
	if len(lines) == 0 {
		both(fmt.Sprintf("Excerpt (%s): (empty)\n", ref))
		return bodySection{kind, n}
	}
	// When the diagnostic comes from the delivery-status part the body only
	// supports it, so a shorter excerpt is enough.
	maxLines := MaxExcerptLines
	if b.DiagnosticSource == mailengine.DiagnosticSourceDSN {
		maxLines = MaxExcerptLines / 2
	}
	from, to := excerptWindow(lines, anchorLine(lines, b), maxLines)
	both(fmt.Sprintf("Excerpt (%s, lines %d-%d of %d):\n", ref, from+1, to, len(lines)))
	if from > 0 {
		both(fmt.Sprintf("[TRUNCATED: %d lines before this excerpt are in %s]\n", from, s.NoticeFile))
	}
	for _, l := range lines[from:to] {
		both("| " + truncateLine(l, maxExcerptLineRunes) + "\n")
	}
	if to < len(lines) {
		both(fmt.Sprintf("[TRUNCATED: continues in %s (%d more lines)]\n", s.NoticeFile, len(lines)-to))
	}
	_, _ = io.WriteString(w, fmt.Sprintf("Body %s (complete, %d lines): in %s\n", ref, len(lines), s.NoticeFile))
	return bodySection{kind, n}
}

// buildDMARCSample renders a sample that is a failing record of a DMARC
// aggregate report: the report (who reported, the period, the published
// policy) and every field of the record. The report mail has no body worth
// reading, so the prompt block and the evidence file are the same.
func buildDMARCSample(s *Sample, in EvidenceInput, b *models.GroupBounce, w io.Writer) {
	r := b.DMARC
	// Every value comes from the report, which anybody can send: folded
	// onto one line, neutralized and capped like a notice line.
	v := func(s string) string { return orNotFound(truncateLine(sanitize(foldLine(s)), maxExcerptLineRunes)) }
	var out strings.Builder
	out.WriteString(fmt.Sprintf("[%s] pattern %s, DMARC report mail %s, %s\n", s.ID, s.Pattern.ID, b.MessageKey, formatTime(models.NullTime(b.Date))))
	out.WriteString("Classification: category " + orPlaceholder(foldLine(in.Group.Category), "(not classified)") +
		", rule " + orPlaceholder(foldLine(b.CategoryRule), "(not recorded)") + "\n")
	out.WriteString("Report: reported by " + v(r.ReportOrg) + "; report ID " + v(r.ReportID) +
		"; period " + orNotFound(formatTime(r.BeginAt)) + " to " + orNotFound(formatTime(r.EndAt)) +
		"; published policy " + v(r.PolicyDomain) + " p=" + v(r.Policy) + "\n")
	out.WriteString("Record: source IP " + v(r.SourceIP) + "; messages " + fmt.Sprint(r.MessageCount) +
		"; header_from " + v(r.HeaderFrom) + "; envelope_from " + v(r.EnvelopeFrom) +
		"; disposition " + v(r.Disposition) + "\n")
	out.WriteString("Policy evaluated (aligned results): dkim " + v(r.DKIMResult) + "; spf " + v(r.SPFResult) + "\n")
	out.WriteString("Auth results: DKIM " + v(r.DKIMAuth) + "; SPF " + v(r.SPFAuth) + "\n")
	s.Prompt = out.String()
	_, _ = io.WriteString(w, s.Prompt)
}

// dnsFailureEvidence renders the failed DNS lookup the diagnostic of a
// bounce reports (mailengine.ParseDNSFailure, the detection the grouping
// uses) and the verification MailCare stored for its name (check; nil or
// ok = false: "(not found)"): the block for the prompt, with at most
// maxDNSEvidenceLines evidence lines, and the block for the evidence file,
// with all of them. Both are "" when the diagnostic reports no failed
// lookup. The evidence lines quote resolvers, name servers and the records
// they returned, which whoever runs a DNS server can fill with any text, so
// every line is folded and prefixed with "| " like the body of a notice.
func dnsFailureEvidence(check DNSCheckFunc, b *models.GroupBounce, fileName string) (prompt, file string) {
	f, found := mailengine.ParseDNSFailure(b.Diagnostic, b.RemoteMTA)
	if !found {
		return "", ""
	}
	name := f.Name
	nameNote := ""
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(b.RecipientDomain))
		nameNote = " (the recipient domain: the notice names no name)"
	}
	qtype := f.QType
	if qtype == "" {
		qtype = "(not named by the notice)"
	}
	wording := `permanent (a definite answer: the resolver of the reporting MTA typically got NXDOMAIN, or the name exists without the records it needed)`
	if f.Temporary {
		wording = `temporary ("try again", "lookup did not complete" or the like: the resolver of the reporting MTA got no usable answer, typically SERVFAIL or a timeout)`
	}
	facts := "DNS lookup failure (parsed from the diagnostic): name " + orNotFound(sanitize(foldLine(name))) + nameNote +
		"; record type " + qtype + " (information only: MailCare verifies the name whatever the type)" +
		"; wording " + wording + "; reporting MTA " + orNotFound(sanitize(foldLine(b.ReportingMTA))) + "\n"
	var res DNSCheckResult
	ok := false
	if check != nil && name != "" {
		res, ok = check(name, noticeTime(b))
	}
	if !ok {
		none := facts + "DNS verification: (not found: this name was not verified by MailCare)\n"
		return none, none
	}
	authoritative := "no (resolvers only)"
	if res.Direct {
		authoritative = "yes"
	}
	head := facts + "DNS verification by MailCare: outcome " + orNotFound(truncateLine(sanitize(foldLine(res.Outcome)), maxDNSOutcomeRunes)) +
		"; checked " + orNotFound(formatTime(models.NullTime(res.CheckedAt))) +
		"; notice received " + orNotFound(formatTime(models.NullTime(noticeTime(b)))) +
		lateCheckNote(res.CheckedAt, noticeTime(b)) +
		"; authoritative servers asked " + authoritative + "\n"
	lines := dnsDetailLines(res.Detail)
	if len(lines) == 0 {
		none := head + "Verification evidence: (not found: no evidence lines were stored)\n"
		return none, none
	}
	const origin = "resolver and name server answers, data from DNS responses"
	var p, fl strings.Builder
	shown := lines
	if len(shown) > maxDNSEvidenceLines {
		shown = shown[:maxDNSEvidenceLines]
	}
	p.WriteString(head)
	p.WriteString(fmt.Sprintf("Verification evidence (lines 1-%d of %d; %s):\n", len(shown), len(lines), origin))
	for _, l := range shown {
		p.WriteString("| " + truncateLine(l, maxExcerptLineRunes) + "\n")
	}
	if len(shown) < len(lines) {
		p.WriteString(fmt.Sprintf("[TRUNCATED: continues in %s (%d more lines)]\n", fileName, len(lines)-len(shown)))
	}
	// The evidence file has every line whole: MailCare wrote them, one per
	// resolver and name server asked, each from one DNS message (at most
	// 65535 octets, RFC 1035 4.2.2).
	fl.WriteString(head)
	fl.WriteString(fmt.Sprintf("Verification evidence (complete, %d lines; %s):\n", len(lines), origin))
	for _, l := range lines {
		fl.WriteString("| " + l + "\n")
	}
	return p.String(), fl.String()
}

// lateCheckNote says how long after the notice the verification ran when
// that is more than mailengine.DNSLateCheckAfter ("" otherwise): the DNS of
// the name may have changed since the failure.
func lateCheckNote(checkedAt, noticeAt time.Time) string {
	if checkedAt.IsZero() || noticeAt.IsZero() {
		return ""
	}
	late := checkedAt.Sub(noticeAt)
	if late <= mailengine.DNSLateCheckAfter {
		return ""
	}
	days := int(late / (24 * time.Hour))
	unit := "days"
	if days == 1 {
		unit = "day"
	}
	return fmt.Sprintf("; checked %d %s after the notice (later than the failure: the DNS of the name may have changed since)", days, unit)
}

// dnsDetailLines splits the stored evidence of a verification into lines,
// each folded onto one line (no line break, control white space or Unicode
// line separator survives) with the output markers neutralized; blank lines
// are dropped.
func dnsDetailLines(detail string) []string {
	var out []string
	for _, l := range strings.Split(detail, "\n") {
		if l = sanitize(foldLine(l)); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// noticeTime is when a notice was received, taken as the grouping takes it
// to pick the stored DNS verification: the IMAP INTERNALDATE when known,
// else the date of the message.
func noticeTime(b *models.GroupBounce) time.Time {
	if b.ReceivedAt.Valid && !b.ReceivedAt.Time.IsZero() {
		return b.ReceivedAt.Time
	}
	return b.Date
}

// Limits of the evidence, for the size of what the agent reads (see the
// limits in agent.go); each cut is marked with what it left out.
const (
	// maxDNSEvidenceLines caps the evidence lines of a DNS verification in
	// the prompt (the evidence file has them all, marked [TRUNCATED] in
	// the prompt); maxDNSOutcomeRunes caps its outcome (one word when it
	// is valid).
	maxDNSEvidenceLines = 12
	maxDNSOutcomeRunes  = 40
	// maxExcerptLineRunes caps one line of a prompt excerpt (the evidence
	// file has the line whole).
	maxExcerptLineRunes = 300
	// maxEvidenceFileLines and maxEvidenceLineRunes cap the body of an
	// evidence file: a file the agent reads goes whole into its context,
	// and a notice body can run to megabytes (a returned attachment as
	// text). The diagnostic and its surroundings are in the first lines;
	// what is cut is counted ("[TRUNCATED: N more lines not included]").
	// No standard defines these values; they are the owner's.
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
	case source == mailengine.DiagnosticSourceDMARC:
		return "a record of a DMARC aggregate report"
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

// deliveryStatusEvidence renders the delivery-status part for a sample: one
// line with the per-recipient block of the bounce's recipient (else the
// first), naming the notice file that holds every block.
func deliveryStatusEvidence(ds *mailengine.DeliveryStatus, recipient, noticeFile string) string {
	if ds == nil {
		return "Delivery status: (not found: the notice has no delivery-status part)\n"
	}
	var pick *mailengine.DeliveryStatusRecipient
	picked := 0
	for i := range ds.Recipients {
		r := &ds.Recipients[i]
		if strings.EqualFold(r.FinalRecipient, recipient) || strings.EqualFold(r.OriginalRecipient, recipient) {
			pick, picked = r, i
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
	line := "Delivery status: " + strings.Join(fields, "; ")
	if len(ds.Recipients) > 1 {
		line += fmt.Sprintf(" (recipient block %d of %d; every block is in %s)", picked+1, len(ds.Recipients), noticeFile)
	}
	return line + "\n"
}

// writeDeliveryStatusBlocks writes every field of every block of the
// delivery-status part (the notice file).
func writeDeliveryStatusBlocks(w io.Writer, ds *mailengine.DeliveryStatus) {
	if ds == nil {
		_, _ = io.WriteString(w, "Delivery status: (not found: the notice has no delivery-status part)\n")
		return
	}
	_, _ = io.WriteString(w, "Delivery status (every field):\n")
	_, _ = io.WriteString(w, "  Reporting-MTA: "+orNotFound(sanitize(foldLine(ds.ReportingMTA)))+"\n")
	_, _ = io.WriteString(w, "  Arrival-Date: "+orNotFound(sanitize(foldLine(ds.ArrivalDate)))+"\n")
	for i := range ds.Recipients {
		fmt.Fprintf(w, "  Recipient block %d:\n", i+1)
		for _, f := range recipientFields(&ds.Recipients[i]) {
			_, _ = io.WriteString(w, "    "+f+"\n")
		}
	}
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

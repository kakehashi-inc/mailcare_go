package mailengine

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"mailcare/app/models"
)

// DMARC aggregate reports (RFC 7489 section 7.2 and appendix C).
//
// A receiver that applies the DMARC policy of a domain sends the domain's
// rua address one report per period: an XML document (<feedback>), usually
// zipped or gzipped, attached to a mail. The report lists, per sending IP
// and identifiers, how many messages the receiver saw and how they fared
// (DKIM and SPF, the disposition it applied). The parser reads the document
// from any attachment of the mail; the classifier treats a mail that carries
// one as a notice of its own kind (bounce kind "report"), and the grouping
// phase files every record that failed DMARC (neither DKIM nor SPF passed
// with alignment) into an actionable group (dmarc_records, design document
// "mail classification").

// DMARCReport is the content of one aggregate report.
type DMARCReport struct {
	OrgName  string    // report_metadata/org_name: the receiver that sent the report
	ReportID string    // report_metadata/report_id
	Begin    time.Time // report_metadata/date_range (UTC; zero when absent)
	End      time.Time
	Domain   string // policy_published/domain (lower case)
	Policy   string // policy_published/p
	Records  []DMARCRecord
}

// DMARCRecord is one <record> of a report: the messages of one sending IP
// with the same identifiers and results.
type DMARCRecord struct {
	SourceIP     string
	Count        int
	Disposition  string // policy_evaluated/disposition: none | quarantine | reject
	DKIM         string // policy_evaluated/dkim: the aligned DKIM result (pass | fail)
	SPF          string // policy_evaluated/spf: the aligned SPF result (pass | fail)
	HeaderFrom   string // identifiers/header_from (lower case)
	EnvelopeFrom string // identifiers/envelope_from (lower case)
	DKIMResults  []DMARCAuthResult
	SPFResults   []DMARCAuthResult
}

// DMARCAuthResult is one auth_results entry: the domain checked, its raw
// result and, for DKIM, the selector or, for SPF, the scope (mfrom / helo).
type DMARCAuthResult struct {
	Domain string
	Result string
	Scope  string
}

// Failed reports whether the record failed DMARC: neither DKIM nor SPF
// passed with alignment.
func (r *DMARCRecord) Failed() bool {
	return r.DKIM != "pass" && r.SPF != "pass"
}

// maxDMARCReportSize bounds the size of a report document, and the total a
// zip attachment may decompress over all the members tried. A compressed
// attachment can expand without limit (a zip bomb); real reports stay far
// below this, so a larger document is not treated as a report.
const maxDMARCReportSize = 64 << 20

// maxDMARCZipMembers bounds how many members of a zip attachment are tried.
const maxDMARCZipMembers = 16

// errDMARCTooLarge is returned when a document exceeds its budget.
var errDMARCTooLarge = errors.New("mailengine: DMARC report document too large")

// parseDMARCAttachment reads a DMARC aggregate report from the bytes of one
// attachment: a zip archive (the first of at most maxDMARCZipMembers members
// that parses, within maxDMARCReportSize decompressed bytes in total), a
// gzip stream or a bare XML document, recognized by their content rather
// than by the declared type or the file name. nil means the attachment is no
// report.
func parseDMARCAttachment(data []byte) *DMARCReport {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil
		}
		budget := int64(maxDMARCReportSize)
		tried := 0
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			if tried++; tried > maxDMARCZipMembers || budget <= 0 {
				return nil
			}
			rc, err := f.Open()
			if err != nil {
				continue
			}
			doc, err := readLimited(rc, budget)
			rc.Close()
			budget -= int64(len(doc))
			if err != nil {
				return nil
			}
			if rep := parseDMARCXML(doc); rep != nil {
				return rep
			}
		}
		return nil
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		gr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		defer gr.Close()
		doc, err := readLimited(gr, maxDMARCReportSize)
		if err != nil {
			return nil
		}
		return parseDMARCXML(doc)
	}
	if len(data) > maxDMARCReportSize {
		return nil
	}
	return parseDMARCXML(data)
}

// readLimited reads r whole unless it exceeds limit bytes. A document is
// abandoned after its first bytes when they show it is no report: it does
// not start like XML (after a BOM and white space), or its root element,
// found within those bytes, is not <feedback> (the XML members of Office
// documents, for instance), so a member that is no report costs little.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	br := bufio.NewReader(io.LimitReader(r, limit+1))
	head, _ := br.Peek(dmarcHeadSize)
	if !mayBeDMARCReport(head) {
		return nil, nil
	}
	doc, err := io.ReadAll(br)
	if err != nil {
		return nil, err
	}
	if int64(len(doc)) > limit {
		return doc, errDMARCTooLarge
	}
	return doc, nil
}

// dmarcHeadSize is how many leading bytes of a document mayBeDMARCReport
// looks at.
const dmarcHeadSize = 4096

// mayBeDMARCReport reports whether the leading bytes of a document can
// start a DMARC report: they start like XML and the root element, when it
// appears within them, is <feedback> (any namespace). When the prolog is too
// long for the root element to appear, the document is read whole.
func mayBeDMARCReport(head []byte) bool {
	trimmed := bytes.TrimLeft(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")), " \t\r\n")
	if len(trimmed) == 0 {
		return true
	}
	if trimmed[0] != '<' {
		return false
	}
	dec := xml.NewDecoder(bytes.NewReader(trimmed))
	dec.CharsetReader = charsetReader
	for {
		tok, err := dec.RawToken()
		if err != nil {
			return true
		}
		if start, ok := tok.(xml.StartElement); ok {
			return start.Name.Local == "feedback"
		}
	}
}

// dmarcFeedback mirrors the parts of the aggregate report schema MailCare
// reads. Element names match in any namespace (DMARC 1 reports carry none,
// DMARCbis reports urn:ietf:params:xml:ns:dmarc-2.0).
type dmarcFeedback struct {
	XMLName  xml.Name `xml:"feedback"`
	Metadata *struct {
		OrgName   string `xml:"org_name"`
		ReportID  string `xml:"report_id"`
		DateRange struct {
			Begin string `xml:"begin"`
			End   string `xml:"end"`
		} `xml:"date_range"`
	} `xml:"report_metadata"`
	Policy *struct {
		Domain string `xml:"domain"`
		P      string `xml:"p"`
	} `xml:"policy_published"`
	Records []struct {
		Row struct {
			SourceIP        string `xml:"source_ip"`
			Count           string `xml:"count"`
			PolicyEvaluated struct {
				Disposition string `xml:"disposition"`
				DKIM        string `xml:"dkim"`
				SPF         string `xml:"spf"`
			} `xml:"policy_evaluated"`
		} `xml:"row"`
		Identifiers struct {
			HeaderFrom   string `xml:"header_from"`
			EnvelopeFrom string `xml:"envelope_from"`
		} `xml:"identifiers"`
		AuthResults struct {
			DKIM []struct {
				Domain   string `xml:"domain"`
				Result   string `xml:"result"`
				Selector string `xml:"selector"`
			} `xml:"dkim"`
			SPF []struct {
				Domain string `xml:"domain"`
				Result string `xml:"result"`
				Scope  string `xml:"scope"`
			} `xml:"spf"`
		} `xml:"auth_results"`
	} `xml:"record"`
}

// parseDMARCXML decodes a report document. Only a <feedback> root with both
// report_metadata and policy_published counts as a report; anything else
// (another XML document, a broken one) yields nil.
func parseDMARCXML(doc []byte) *DMARCReport {
	trimmed := bytes.TrimLeft(bytes.TrimPrefix(doc, []byte("\xef\xbb\xbf")), " \t\r\n")
	if !bytes.HasPrefix(trimmed, []byte("<")) {
		return nil
	}
	dec := xml.NewDecoder(bytes.NewReader(trimmed))
	dec.CharsetReader = charsetReader
	var fb dmarcFeedback
	if err := dec.Decode(&fb); err != nil || fb.Metadata == nil || fb.Policy == nil {
		return nil
	}
	rep := &DMARCReport{
		OrgName:  strings.TrimSpace(fb.Metadata.OrgName),
		ReportID: strings.TrimSpace(fb.Metadata.ReportID),
		Begin:    unixTime(fb.Metadata.DateRange.Begin),
		End:      unixTime(fb.Metadata.DateRange.End),
		Domain:   lowerTrim(fb.Policy.Domain),
		Policy:   lowerTrim(fb.Policy.P),
	}
	for _, x := range fb.Records {
		count, _ := strconv.Atoi(strings.TrimSpace(x.Row.Count))
		r := DMARCRecord{
			SourceIP:     strings.TrimSpace(x.Row.SourceIP),
			Count:        count,
			Disposition:  lowerTrim(x.Row.PolicyEvaluated.Disposition),
			DKIM:         lowerTrim(x.Row.PolicyEvaluated.DKIM),
			SPF:          lowerTrim(x.Row.PolicyEvaluated.SPF),
			HeaderFrom:   lowerTrim(x.Identifiers.HeaderFrom),
			EnvelopeFrom: lowerTrim(x.Identifiers.EnvelopeFrom),
		}
		for _, d := range x.AuthResults.DKIM {
			r.DKIMResults = append(r.DKIMResults, DMARCAuthResult{Domain: lowerTrim(d.Domain), Result: lowerTrim(d.Result), Scope: strings.TrimSpace(d.Selector)})
		}
		for _, s := range x.AuthResults.SPF {
			r.SPFResults = append(r.SPFResults, DMARCAuthResult{Domain: lowerTrim(s.Domain), Result: lowerTrim(s.Result), Scope: lowerTrim(s.Scope)})
		}
		rep.Records = append(rep.Records, r)
	}
	return rep
}

func lowerTrim(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// unixTime converts a date_range value (seconds since the epoch) to UTC; an
// empty or invalid value is the zero time.
func unixTime(s string) time.Time {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// organizationalDomain returns the registrable domain of a name (the public
// suffix plus one label, "mail.example.co.jp" -> "example.co.jp"); a name
// the public suffix list cannot place is returned as it is.
func organizationalDomain(name string) string {
	name = strings.TrimSuffix(lowerTrim(name), ".")
	if name == "" {
		return ""
	}
	if org, err := publicsuffix.EffectiveTLDPlusOne(name); err == nil {
		return org
	}
	return name
}

// dmarcAligned reports whether two domains are aligned in relaxed mode
// (same organizational domain, RFC 7489 section 3.1).
func dmarcAligned(a, b string) bool {
	oa := organizationalDomain(a)
	return oa != "" && oa == organizationalDomain(b)
}

// Category rules of the DMARC categories (dmarc_records.category_rule).
const (
	dmarcRuleSPFMissing    = "dmarc_spf_missing"      // the aligned SPF domain has no usable SPF record
	dmarcRuleDKIMFailed    = "dmarc_dkim_failed"      // an aligned DKIM signature did not verify
	dmarcRuleStrictAlign   = "dmarc_strict_alignment" // an aligned identifier passed, DMARC still failed (strict alignment)
	dmarcRuleNotAuthorized = "dmarc_not_authorized"   // an aligned SPF domain does not authorize the IP, no aligned DKIM signature
	dmarcRuleUnaligned     = "dmarc_unaligned"        // neither DKIM nor SPF names an aligned domain
)

// dmarcSPFMissingResults are the SPF results that say the domain publishes
// no usable SPF record.
var dmarcSPFMissingResults = map[string]bool{"none": true, "neutral": true, "temperror": true, "permerror": true}

// CategorizeDMARCRecord files a record that failed DMARC into a category
// (design document "mail classification"). Whether the source is a server
// of the domain, a forwarder or somebody spoofing the domain cannot be told
// from a report, so every category is actionable; the categories only say
// what the record shows, which is the starting point of the check:
//
//   - dmarc_spf_missing: an aligned SPF domain (the domain or a subdomain,
//     e.g. the HELO name of a server) publishes no usable SPF record (none,
//     neutral, temperror, permerror) and no aligned DKIM signature verified;
//   - dmarc_dkim_failed: the mail carried a DKIM signature of the domain
//     that did not verify (changed on its way by a forwarder or a security
//     gateway, or a wrong key on the sending side);
//   - dmarc_not_authenticated: anything else: no aligned DKIM signature and
//     an SPF domain that is not aligned or does not authorize the IP (a
//     service missing from the SPF record, or a sender not entitled to use
//     the domain), or an aligned identifier that passed while DMARC still
//     failed (strict alignment).
//
// The group is the category and the domain of the record (header_from, else
// the published domain); the sending IPs and the results are kept on the
// records and in the pattern.
func CategorizeDMARCRecord(rep *DMARCReport, r *DMARCRecord) Category {
	domain := r.HeaderFrom
	if domain == "" {
		domain = rep.Domain
	}
	build := func(category, reason string) Category {
		unit := domain
		if strings.TrimSpace(unit) == "" {
			unit = unitUnknown
		}
		def := categoryDefs[category]
		return Category{Category: category, UnitValue: unit, Actionable: def.actionable,
			Responsible: def.responsible, Reason: reason}
	}
	for _, d := range r.DKIMResults {
		if dmarcAligned(d.Domain, domain) && d.Result == "pass" {
			return build(categoryDMARCNotAuthenticated, dmarcRuleStrictAlign)
		}
	}
	for _, s := range r.SPFResults {
		if dmarcAligned(s.Domain, domain) && s.Result == "pass" {
			return build(categoryDMARCNotAuthenticated, dmarcRuleStrictAlign)
		}
	}
	for _, s := range r.SPFResults {
		if dmarcAligned(s.Domain, domain) && dmarcSPFMissingResults[s.Result] {
			return build(categoryDMARCSPFMissing, dmarcRuleSPFMissing)
		}
	}
	for _, d := range r.DKIMResults {
		// "none" is no signature at all, not one that failed to verify.
		if dmarcAligned(d.Domain, domain) && d.Result != "none" {
			return build(categoryDMARCDKIMFailed, dmarcRuleDKIMFailed)
		}
	}
	for _, s := range r.SPFResults {
		if dmarcAligned(s.Domain, domain) {
			return build(categoryDMARCNotAuthenticated, dmarcRuleNotAuthorized)
		}
	}
	return build(categoryDMARCNotAuthenticated, dmarcRuleUnaligned)
}

// DMARCTemplate is the shape of a failing record: the results of the
// identifiers aligned with the domain, whether any other identifier was
// checked, and the disposition. Sending IPs, counts, dates, reporters and
// the names of foreign domains are left out, so that the same kind of
// failure from ever new sources stays one pattern of its group (a group is
// analyzed again only for a new pattern).
func DMARCTemplate(rep *DMARCReport, r *DMARCRecord) string {
	domain := r.HeaderFrom
	if domain == "" {
		domain = rep.Domain
	}
	return fmt.Sprintf("dmarc fail: dkim %s; spf %s; disposition %s",
		alignedResults(r.DKIMResults, domain), alignedResults(r.SPFResults, domain), orNone(r.Disposition))
}

// alignedResults renders the results of the entries aligned with domain
// ("aligned fail", "aligned none, unaligned pass"), "unaligned" when only
// other domains were checked and "none" when nothing was.
func alignedResults(results []DMARCAuthResult, domain string) string {
	if len(results) == 0 {
		return "none"
	}
	var aligned []string
	unaligned := false
	seen := map[string]bool{}
	for _, a := range results {
		if !dmarcAligned(a.Domain, domain) {
			unaligned = true
			continue
		}
		if v := orNone(a.Result); !seen[v] {
			seen[v] = true
			aligned = append(aligned, v)
		}
	}
	var parts []string
	if len(aligned) > 0 {
		parts = append(parts, "aligned "+strings.Join(aligned, "/"))
	}
	if unaligned {
		parts = append(parts, "unaligned")
	}
	return strings.Join(parts, ", ")
}

// authList renders auth_results entries as "domain result, ..." ("none"
// when the record has none): the full detail kept on the record.
func authList(results []DMARCAuthResult) string {
	if len(results) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(results))
	for _, a := range results {
		parts = append(parts, orNone(a.Domain)+" "+orNone(a.Result))
	}
	return strings.Join(parts, ", ")
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// dmarcRecordsFor builds the dmarc_records rows of a report and the group
// of each: one row per record that failed DMARC (passing records are not
// kept). The pattern of a row is its template (PatternKey with
// DiagnosticSourceDMARC).
func dmarcRecordsFor(rep *DMARCReport) ([]*models.DMARCRecord, []*models.BounceGroup) {
	if rep == nil {
		return nil, nil
	}
	var rows []*models.DMARCRecord
	var groups []*models.BounceGroup
	for i := range rep.Records {
		r := &rep.Records[i]
		if !r.Failed() {
			continue
		}
		c := CategorizeDMARCRecord(rep, r)
		template := DMARCTemplate(rep, r)
		g := &models.BounceGroup{
			GroupKey:           GroupKey(c.Category, c.UnitValue, c.Authority),
			Category:           c.Category,
			UnitValue:          groupKeyPart(c.UnitValue),
			Authority:          groupKeyPart(c.Authority),
			Actionable:         c.Actionable,
			DiagnosticTemplate: template,
			Responsible:        c.Responsible,
		}
		rows = append(rows, &models.DMARCRecord{
			GroupKey:           g.GroupKey,
			CategoryRule:       c.Reason,
			DiagnosticTemplate: template,
			PatternKey:         PatternKey("", template, "", DiagnosticSourceDMARC),
			ReportOrg:          rep.OrgName,
			ReportID:           rep.ReportID,
			BeginAt:            models.NullTime(rep.Begin),
			EndAt:              models.NullTime(rep.End),
			PolicyDomain:       rep.Domain,
			Policy:             rep.Policy,
			HeaderFrom:         r.HeaderFrom,
			EnvelopeFrom:       r.EnvelopeFrom,
			SourceIP:           r.SourceIP,
			MessageCount:       r.Count,
			Disposition:        r.Disposition,
			DKIMResult:         r.DKIM,
			SPFResult:          r.SPF,
			DKIMAuth:           authList(r.DKIMResults),
			SPFAuth:            authList(r.SPFResults),
		})
		groups = append(groups, g)
	}
	return rows, groups
}

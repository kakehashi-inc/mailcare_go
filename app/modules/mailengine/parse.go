package mailengine

import (
	"bytes"
	"fmt"
	"html"
	"io"
	netmail "net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // registers ISO-2022-JP, Shift_JIS, EUC-JP and friends
	"github.com/emersion/go-message/mail"
)

// ParsedMessage is what the MIME parser makes of one raw message: the
// headers the index row needs, the body sections the classifier and the
// extractor look at (and that become the .txt / .html files) and the
// structured parts of a delivery report. It only lives in memory: the index
// row keeps the headers and the section counts, the section files keep the
// bodies, and everything is parsed again from the .eml when needed.
type ParsedMessage struct {
	MessageID     string
	Subject       string
	FromAddress   string
	FromName      string
	To            string
	ToName        string // display name of the first To address
	Date          time.Time
	ReturnPath    string
	AutoSubmitted string
	ContentType   string // media type only, lower case
	ReportType    string // report-type parameter of multipart/report
	// Headers holds every top-level header (canonical key, decoded, values
	// joined with a newline).
	Headers map[string]string

	// TextSections / HTMLSections are the decoded (UTF-8) bodies of the
	// inline text/plain and text/html parts that carry content (for HTML:
	// after stripping the tags), in MIME order; parts inside an embedded
	// original message and attachments are left out. They are written as
	// <key>-1.txt, <key>-2.txt, ... and <key>-1.html, <key>-2.html, ... and
	// counted in messages.text_count / html_count (design 5.2). BodySource
	// names the body the classifier and the extractor look at first ("text"
	// when there are text sections, else "html" when there are HTML
	// sections, else ""); the grouping phase records the body actually used
	// in the index row (Classification.BodySource).
	TextSections []string
	HTMLSections []string
	BodySource   string

	DeliveryStatus  *DeliveryStatus
	OriginalMessage *OriginalMessage

	// ParseError records why the MIME parser gave up; the fields above hold
	// whatever could still be read.
	ParseError string

	parts    int    // leaf parts seen by walk
	textBody string // TextSections joined (computed by finishBodies)
	htmlText string // HTMLSections rendered as text and joined (computed by finishBodies)
}

// TextCount is the number of text sections (messages.text_count).
func (pm *ParsedMessage) TextCount() int { return len(pm.TextSections) }

// HTMLCount is the number of HTML sections (messages.html_count).
func (pm *ParsedMessage) HTMLCount() int { return len(pm.HTMLSections) }

// Body sources (ParsedMessage.BodySource / messages.body_source).
const (
	bodySourceText = "text"
	bodySourceHTML = "html"
)

// Source is the IMAP identity and the fetch facts of a message handed to
// storeMessage: what the fetch saw, or what Reindex carried over from the
// previous index (or synthesized) for a raw file.
type Source struct {
	MessageKey  string // "" lets storeMessage derive the key
	Folder      string
	UIDValidity uint32
	UID         uint32
	Size        int64
	ReceivedAt  time.Time // IMAP INTERNALDATE
	FetchedAt   time.Time
}

// DeliveryStatus is the structured message/delivery-status part (RFC 3464).
type DeliveryStatus struct {
	ReportingMTA string
	ArrivalDate  string
	Recipients   []DeliveryStatusRecipient
}

// DeliveryStatusRecipient is one per-recipient block of a delivery-status
// part. Action and Status are normalized by the parser (normalizeDSNAction /
// normalizeDSNStatus): Action is one of failed / delayed / delivered /
// relayed / expanded or "", Status an extended status code (5.1.1) or "".
type DeliveryStatusRecipient struct {
	FinalRecipient    string
	OriginalRecipient string
	Action            string
	Status            string
	DiagnosticCode    string
	RemoteMTA         string
}

// OriginalMessage holds the headers of the message a bounce refers to, taken
// from a message/rfc822 or text/rfc822-headers part.
type OriginalMessage struct {
	MessageID string
	Subject   string
	From      string
	Date      time.Time
}

// Nothing about a message is capped: every header value, every body section
// and every nesting level is read whole, whatever its size, so no mail is
// shortened or dropped.

// ParseMessage parses a raw RFC 5322 message. It never panics and never
// returns a nil message: on a broken message the returned ParsedMessage holds
// whatever headers could be read and ParseError describes the failure.
func ParseMessage(raw []byte) *ParsedMessage {
	pm := parseMessage(raw)
	pm.finishBodies()
	return pm
}

// parseMessage is ParseMessage without the final body bookkeeping.
func parseMessage(raw []byte) (pm *ParsedMessage) {
	pm = &ParsedMessage{Headers: map[string]string{}}
	defer func() {
		if r := recover(); r != nil {
			pm.ParseError = fmt.Sprintf("panic while parsing: %v", r)
			pm.fillFallbackHeaders(raw)
		}
	}()
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		pm.ParseError = err.Error()
		if entity == nil {
			pm.fillFallbackHeaders(raw)
			return pm
		}
	}
	if entity == nil {
		pm.ParseError = "no message entity"
		pm.fillFallbackHeaders(raw)
		return pm
	}
	pm.readTopHeaders(entity.Header)
	if err := pm.walk(entity, false); err != nil && pm.ParseError == "" {
		pm.ParseError = err.Error()
	}
	if strings.HasPrefix(pm.ContentType, "multipart/") && pm.parts == 0 {
		// A multipart message whose boundary never appears: keep the raw
		// body as text so that the classifier still sees the wording.
		if pm.ParseError == "" {
			pm.ParseError = "multipart message without any part"
		}
		pm.fillFallbackBody(raw)
	}
	return pm
}

// finishBodies applies the body rule of design 5.2 to the collected
// sections: a section counts only when it has non-blank content (HTML after
// stripping the tags), so blank ones are dropped and no file is written for
// them; the joined views the classifier and the extractor use are computed
// once. BodySource starts as the primary body (text over HTML); the grouping
// phase records the body actually used when only the HTML matched.
func (pm *ParsedMessage) finishBodies() {
	pm.TextSections = keepSections(pm.TextSections, func(s string) bool { return strings.TrimSpace(s) != "" })
	pm.HTMLSections = keepSections(pm.HTMLSections, func(s string) bool { return htmlToText(s) != "" })
	pm.textBody = joinSections(pm.TextSections, "\n\n")
	texts := make([]string, 0, len(pm.HTMLSections))
	for _, s := range pm.HTMLSections {
		texts = append(texts, htmlToText(s))
	}
	pm.htmlText = joinSections(texts, "\n\n")
	pm.BodySource = pm.primarySource()
}

// keepSections returns the sections accepted by keep, in order (nil when
// none are left, so an empty result compares equal to a never-filled one).
func keepSections(sections []string, keep func(string) bool) []string {
	var out []string
	for _, s := range sections {
		if keep(s) {
			out = append(out, s)
		}
	}
	return out
}

// joinSections concatenates body sections into one text: the first is taken
// as is, every later one follows the previous text after sep (one blank
// line, so the extraction rules see paragraph breaks between the parts);
// blank sections add nothing.
func joinSections(sections []string, sep string) string {
	body := ""
	for _, s := range sections {
		if strings.TrimSpace(s) == "" {
			continue
		}
		if strings.TrimSpace(body) == "" {
			body = s
			continue
		}
		body = strings.TrimRight(body, "\n") + sep + strings.TrimLeft(s, "\n")
	}
	return body
}

// addTextSection keeps one inline text/plain part as a section (blank parts
// are dropped; design 5.2).
func (pm *ParsedMessage) addTextSection(s string) {
	if strings.TrimSpace(s) == "" {
		return
	}
	pm.TextSections = append(pm.TextSections, s)
}

// addHTMLSection keeps one inline text/html part as a section unless it is
// only markup (skeleton HTML counts as no content; design 5.2).
func (pm *ParsedMessage) addHTMLSection(s string) {
	if htmlToText(s) == "" {
		return
	}
	pm.HTMLSections = append(pm.HTMLSections, s)
}

// readTopHeaders fills the header fields from the root entity.
func (pm *ParsedMessage) readTopHeaders(h message.Header) {
	mh := mail.Header{Header: h}
	pm.MessageID = headerMessageID(mh)
	pm.Subject = headerText(mh, "Subject")
	pm.FromAddress, pm.FromName = headerAddress(mh, "From")
	pm.To, pm.ToName = headerAddressList(mh, "To")
	if t, err := mh.Date(); err == nil {
		pm.Date = t.UTC()
	} else if raw := h.Get("Date"); raw != "" {
		if t, err := parseLooseDate(raw); err == nil {
			pm.Date = t.UTC()
		}
	}
	pm.ReturnPath = cleanAngleAddress(h.Get("Return-Path"))
	pm.AutoSubmitted = strings.ToLower(strings.TrimSpace(h.Get("Auto-Submitted")))
	ct, params, err := h.ContentType()
	if err == nil {
		pm.ContentType = strings.ToLower(ct)
		pm.ReportType = strings.ToLower(strings.TrimSpace(params["report-type"]))
	} else if raw := h.Get("Content-Type"); raw != "" {
		pm.ContentType = strings.ToLower(strings.TrimSpace(strings.SplitN(raw, ";", 2)[0]))
		pm.ReportType = strings.ToLower(paramFromRaw(raw, "report-type"))
	}
	fields := h.Fields()
	for fields.Next() {
		key := fields.Key()
		val, err := fields.Text()
		if err != nil {
			val = fields.Value()
		}
		val = strings.TrimSpace(val)
		if prev, ok := pm.Headers[key]; ok {
			pm.Headers[key] = prev + "\n" + val
		} else {
			pm.Headers[key] = val
		}
	}
}

// walk descends the MIME tree. inOriginal is true inside a message/rfc822
// part: only the headers of the embedded message are of interest there.
func (pm *ParsedMessage) walk(e *message.Entity, inOriginal bool) error {
	ct, ctParams, err := e.Header.ContentType()
	if err != nil {
		ct = "text/plain"
		ctParams = map[string]string{"charset": paramFromRaw(e.Header.Get("Content-Type"), "charset")}
	}
	ct = strings.ToLower(ct)
	disp, _, _ := e.Header.ContentDisposition()
	isAttachment := strings.EqualFold(disp, "attachment")
	if !strings.HasPrefix(ct, "multipart/") {
		pm.parts++
	}

	switch {
	case strings.HasPrefix(ct, "multipart/"):
		mr := e.MultipartReader()
		if mr == nil {
			return nil
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return nil
			}
			if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
				// A broken boundary or a truncated part: keep what we have.
				return err
			}
			if part == nil {
				return nil
			}
			if err := pm.walk(part, inOriginal); err != nil {
				return err
			}
		}
	case ct == "message/delivery-status":
		if !inOriginal && pm.DeliveryStatus == nil {
			body, _ := readWhole(e.Body)
			pm.DeliveryStatus = parseDeliveryStatus(body)
		} else {
			_, _ = io.Copy(io.Discard, e.Body)
		}
	case ct == "message/rfc822", ct == "message/global":
		if inOriginal || pm.OriginalMessage != nil {
			_, _ = io.Copy(io.Discard, e.Body)
			return nil
		}
		inner, err := message.Read(e.Body)
		if inner == nil {
			return nil
		}
		if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
			pm.OriginalMessage = originalFromHeader(inner.Header)
			return nil
		}
		pm.OriginalMessage = originalFromHeader(inner.Header)
		// The embedded message may itself be multipart; walk it so that its
		// parts are consumed, but nothing inside it is treated as our body.
		return pm.walk(inner, true)
	case ct == "text/rfc822-headers":
		if inOriginal || pm.OriginalMessage != nil {
			_, _ = io.Copy(io.Discard, e.Body)
			return nil
		}
		body, _ := readWhole(e.Body)
		hdr, err := message.Read(bytes.NewReader(append(bytes.TrimLeft(body, "\r\n"), "\r\n\r\n"...)))
		if hdr != nil && (err == nil || message.IsUnknownCharset(err) || message.IsUnknownEncoding(err)) {
			pm.OriginalMessage = originalFromHeader(hdr.Header)
		}
	case ct == "text/plain":
		// Every inline text part becomes a section, in MIME order: some
		// MTAs put the original message's text before the notice, others
		// after it, and the notice must not be lost either way (design 5.2).
		if inOriginal || isAttachment {
			_, _ = io.Copy(io.Discard, e.Body)
			return nil
		}
		body, _ := readWhole(e.Body)
		pm.addTextSection(normalizeText(decodeBodyBytes(body, ctParams["charset"], false)))
	case ct == "text/html":
		if inOriginal || isAttachment {
			_, _ = io.Copy(io.Discard, e.Body)
			return nil
		}
		body, _ := readWhole(e.Body)
		pm.addHTMLSection(normalizeText(decodeBodyBytes(body, ctParams["charset"], true)))
	default:
		_, _ = io.Copy(io.Discard, e.Body)
	}
	return nil
}

// originalFromHeader picks the headers of an embedded original message.
func originalFromHeader(h message.Header) *OriginalMessage {
	mh := mail.Header{Header: h}
	om := &OriginalMessage{
		MessageID: headerMessageID(mh),
		Subject:   headerText(mh, "Subject"),
	}
	addr, name := headerAddress(mh, "From")
	om.From = formatAddress(addr, name)
	if t, err := mh.Date(); err == nil {
		om.Date = t.UTC()
	} else if raw := h.Get("Date"); raw != "" {
		if t, err := parseLooseDate(raw); err == nil {
			om.Date = t.UTC()
		}
	}
	return om
}

// parseDeliveryStatus reads the per-message and per-recipient field groups of
// a message/delivery-status body (groups are separated by blank lines).
func parseDeliveryStatus(body []byte) *DeliveryStatus {
	ds := &DeliveryStatus{}
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	groups := dsGroupSplitRe.Split(strings.TrimSpace(text), -1)
	for _, g := range groups {
		fields := parseFieldGroup(g)
		if len(fields) == 0 {
			continue
		}
		isRecipient := fields["final-recipient"] != "" || fields["action"] != "" || fields["status"] != ""
		if !isRecipient {
			// Per-message fields (the first group in a well-formed part).
			if v := fields["reporting-mta"]; v != "" && ds.ReportingMTA == "" {
				ds.ReportingMTA = stripTypePrefix(v)
			}
			if v := fields["arrival-date"]; v != "" && ds.ArrivalDate == "" {
				ds.ArrivalDate = v
			}
			continue
		}
		ds.Recipients = append(ds.Recipients, DeliveryStatusRecipient{
			FinalRecipient:    cleanAngleAddress(stripTypePrefix(fields["final-recipient"])),
			OriginalRecipient: cleanAngleAddress(stripTypePrefix(fields["original-recipient"])),
			Action:            normalizeDSNAction(fields["action"]),
			Status:            normalizeDSNStatus(fields["status"]),
			DiagnosticCode:    fields["diagnostic-code"],
			RemoteMTA:         stripTypePrefix(fields["remote-mta"]),
		})
	}
	if ds.ReportingMTA == "" && ds.ArrivalDate == "" && len(ds.Recipients) == 0 {
		return nil
	}
	return ds
}

// dsnActions are the Action values of RFC 3464 (bounces.action).
var dsnActions = map[string]bool{"failed": true, "delayed": true, "delivered": true, "relayed": true, "expanded": true}

// dsnCommentRe matches the parenthesized comments an Action or Status field
// may carry ("failed (permanent failure)", "5.1.1 (bad destination)").
var dsnCommentRe = regexp.MustCompile(`\([^()]*\)`)

// dsnStatusRe matches an extended status code (class.subject.detail, class
// 2, 4 or 5) inside a Status field; a fourth component ("5.1.1.1") is not a
// status code.
var dsnStatusRe = regexp.MustCompile(`(?:^|[^0-9.])([245]\.[0-9]{1,3}\.[0-9]{1,3})(?:[^0-9.]|$)`)

// normalizeDSNAction reduces an Action field to one of the RFC 3464 values
// (lower case, comments removed); anything else yields "" so that the
// classifier falls back to the status code and the wording.
func normalizeDSNAction(v string) string {
	v = strings.ToLower(strings.TrimSpace(dsnCommentRe.ReplaceAllString(v, " ")))
	if dsnActions[v] {
		return v
	}
	return ""
}

// normalizeDSNStatus extracts the extended status code (class.subject.detail,
// class 2, 4 or 5) of a Status field; "" when the field carries none.
func normalizeDSNStatus(v string) string {
	if m := dsnStatusRe.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return ""
}

// parseFieldGroup parses "Name: value" lines with RFC 822 style folding into
// a lower-cased map.
func parseFieldGroup(group string) map[string]string {
	out := map[string]string{}
	var lastKey string
	for _, line := range strings.Split(group, "\n") {
		if line == "" {
			continue
		}
		if (line[0] == ' ' || line[0] == '\t') && lastKey != "" {
			out[lastKey] += " " + strings.TrimSpace(line)
			continue
		}
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:i]))
		val := strings.TrimSpace(line[i+1:])
		if _, dup := out[key]; !dup {
			out[key] = val
		}
		lastKey = key
	}
	return out
}

// stripTypePrefix removes the "rfc822;", "smtp;", "dns;" style type prefix of
// a delivery-status value.
func stripTypePrefix(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, ';'); i >= 0 && i <= 12 && !strings.ContainsAny(v[:i], " @") {
		return strings.TrimSpace(v[i+1:])
	}
	return v
}

// cleanAngleAddress turns "<user@example.com>" into "user@example.com".
func cleanAngleAddress(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "<")
	v = strings.TrimSuffix(v, ">")
	return strings.TrimSpace(v)
}

// headerText returns a decoded header value, falling back to the raw value.
func headerText(h mail.Header, key string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(decodeWordsLoose(h.Get(key))), " "))
}

// headerMessageID returns the Message-ID without angle brackets.
func headerMessageID(h mail.Header) string {
	if id, err := h.MessageID(); err == nil && id != "" {
		return id
	}
	return cleanAngleAddress(h.Get("Message-Id"))
}

// headerAddress returns the first address and display name of an address
// header, tolerating malformed values such as "MAILER-DAEMON (Mail Delivery
// System)".
func headerAddress(h mail.Header, key string) (address, name string) {
	if list := parseAddressHeader(h.Get(key)); len(list) > 0 {
		return strings.TrimSpace(list[0].Address), strings.TrimSpace(list[0].Name)
	}
	raw := decodeWordsLoose(h.Get(key))
	return looseAddress(raw)
}

// parseAddressHeader parses a raw address header with the engine's charset
// handling (aliases, mislabeled ASCII, raw 8-bit names). nil when the value
// is malformed.
func parseAddressHeader(raw string) []*netmail.Address {
	raw = asciiWordLabelRe.ReplaceAllString(raw, "=?"+mislabeledASCIILabel+"$1?$2?")
	list, err := headerAddressParser.ParseList(raw)
	if err != nil {
		return nil
	}
	for _, a := range list {
		a.Name = decodeHeaderText(a.Name)
	}
	return list
}

// headerAddressList returns the bare addresses of an address header joined
// by ", " (display names stripped) and the display name of the first one.
// A malformed header that yields no address is returned as is.
func headerAddressList(h mail.Header, key string) (addresses, firstName string) {
	if list := parseAddressHeader(h.Get(key)); len(list) > 0 {
		parts := make([]string, 0, len(list))
		for _, a := range list {
			if addr := strings.TrimSpace(a.Address); addr != "" {
				parts = append(parts, addr)
			}
		}
		return strings.Join(parts, ", "), strings.TrimSpace(list[0].Name)
	}
	return looseAddressList(decodeWordsLoose(h.Get(key)))
}

// looseAddressList is headerAddressList for a raw (already decoded) header
// value that the strict parser rejected.
func looseAddressList(raw string) (addresses, firstName string) {
	addr, name := looseAddress(raw)
	if addr == "" {
		return strings.TrimSpace(raw), name
	}
	return addr, name
}

var (
	dsGroupSplitRe  = regexp.MustCompile(`\n\s*\n`)
	emailInTextRe   = regexp.MustCompile(`[A-Za-z0-9._%+\-=/]+@[A-Za-z0-9.\-]+\.[A-Za-z0-9\-]+`)
	angleAddrRe     = regexp.MustCompile(`<([^<>\s]*)>`)
	parenCommentRe  = regexp.MustCompile(`\(([^()]*)\)`)
	quotedNameRe    = regexp.MustCompile(`^"([^"]*)"`)
	multiSpaceRe    = regexp.MustCompile(`\s+`)
	looseDateLayout = []string{
		time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822,
		"Mon, 2 Jan 2006 15:04:05 -0700", "2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04 -0700",
		"Mon, 02 Jan 2006 15:04:05 -0700 (MST)", "2006-01-02 15:04:05 -0700", time.RFC3339,
	}
)

// looseAddress extracts an address and a display name from a malformed
// address header.
func looseAddress(raw string) (address, name string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if m := angleAddrRe.FindStringSubmatch(raw); m != nil {
		address = m[1]
		name = strings.TrimSpace(strings.Replace(raw, m[0], "", 1))
	} else if m := emailInTextRe.FindString(raw); m != "" {
		address = m
		name = strings.TrimSpace(strings.Replace(raw, m, "", 1))
	} else {
		// "MAILER-DAEMON (Mail Delivery System)": the word is the local part.
		fields := strings.Fields(raw)
		if len(fields) > 0 && !strings.HasPrefix(fields[0], "(") {
			address = fields[0]
			name = strings.TrimSpace(strings.TrimPrefix(raw, fields[0]))
		} else {
			name = raw
		}
	}
	if m := parenCommentRe.FindStringSubmatch(name); m != nil && strings.TrimSpace(m[1]) != "" {
		name = strings.TrimSpace(m[1])
	} else if m := quotedNameRe.FindStringSubmatch(name); m != nil {
		name = strings.TrimSpace(m[1])
	}
	name = strings.Trim(name, `"' `)
	return address, name
}

// decodeWordsLoose decodes RFC 2047 encoded words, leaving the input as is on
// failure.
func decodeWordsLoose(s string) string {
	return decodeHeaderWords(s)
}

// formatAddress renders "Name <addr>" (or just addr / Name).
func formatAddress(addr, name string) string {
	switch {
	case addr == "":
		return name
	case name == "":
		return addr
	default:
		return name + " <" + addr + ">"
	}
}

// parseLooseDate tries a few common non-conforming Date layouts.
func parseLooseDate(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if t, err := netmail.ParseDate(raw); err == nil {
		return t, nil
	}
	for _, layout := range looseDateLayout {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized date %q", raw)
}

// paramFromRaw extracts a parameter from a raw Content-Type value when the
// strict parser rejected it.
func paramFromRaw(raw, name string) string {
	for _, p := range strings.Split(raw, ";")[1:] {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) == 2 && strings.EqualFold(strings.TrimSpace(kv[0]), name) {
			return strings.Trim(strings.TrimSpace(kv[1]), `"`)
		}
	}
	return ""
}

// readWhole reads r to the end (tolerating unknown charset / encoding errors).
func readWhole(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	b, err := io.ReadAll(r)
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return b, err
	}
	return b, nil
}

// normalizeText turns a decoded body into text with LF line endings and no
// BOM. go-message already converted known charsets to UTF-8; a body in an
// unknown charset arrives as raw bytes and is kept as is (the .txt / .html
// file then holds the original bytes so nothing is lost), which the
// classifier and the extractor tolerate.
func normalizeText(b []byte) string {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	return strings.TrimPrefix(s, "\ufeff")
}

// fillFallbackHeaders scans the raw header block with a tolerant line parser
// when the MIME parser failed entirely.
func (pm *ParsedMessage) fillFallbackHeaders(raw []byte) {
	head, _ := splitHeaderBlock(raw)
	fields := parseFieldGroup(strings.ReplaceAll(string(head), "\r\n", "\n"))
	get := func(k string) string { return decodeWordsLoose(fields[k]) }
	if pm.Subject == "" {
		pm.Subject = get("subject")
	}
	if pm.MessageID == "" {
		pm.MessageID = cleanAngleAddress(fields["message-id"])
	}
	if pm.FromAddress == "" && pm.FromName == "" {
		pm.FromAddress, pm.FromName = looseAddress(get("from"))
	}
	if pm.To == "" {
		pm.To, pm.ToName = looseAddressList(get("to"))
	}
	if pm.Date.IsZero() {
		if t, err := parseLooseDate(fields["date"]); err == nil {
			pm.Date = t.UTC()
		}
	}
	if pm.ReturnPath == "" {
		pm.ReturnPath = cleanAngleAddress(fields["return-path"])
	}
	if pm.AutoSubmitted == "" {
		pm.AutoSubmitted = strings.ToLower(fields["auto-submitted"])
	}
	if pm.ContentType == "" {
		if raw := fields["content-type"]; raw != "" {
			pm.ContentType = strings.ToLower(strings.TrimSpace(strings.SplitN(raw, ";", 2)[0]))
			pm.ReportType = strings.ToLower(paramFromRaw(raw, "report-type"))
		}
	}
	if len(pm.Headers) == 0 {
		for k, v := range fields {
			pm.Headers[canonicalHeaderKey(k)] = decodeWordsLoose(v)
		}
	}
	pm.fillFallbackBody(raw)
}

// fillFallbackBody keeps the bytes after the header block as the only text
// section so that the classifier still has something to look at.
func (pm *ParsedMessage) fillFallbackBody(raw []byte) {
	if len(pm.TextSections) > 0 {
		return
	}
	head, body := splitHeaderBlock(raw)
	if len(body) == 0 {
		return
	}
	body = bytes.TrimLeft(body, "\r\n")
	fields := parseFieldGroup(strings.ReplaceAll(string(head), "\r\n", "\n"))
	label := paramFromRaw(fields["content-type"], "charset")
	if label != "" && !asciiLabels[normalizeCharsetLabel(label)] {
		body = decodeBytes(label, body)
	} else {
		body = decodeBodyBytes(body, label, strings.HasPrefix(pm.ContentType, "text/html"))
	}
	pm.addTextSection(normalizeText(body))
}

// splitHeaderBlock separates the header block from the body at the first
// blank line.
func splitHeaderBlock(raw []byte) (head, body []byte) {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[:i], raw[i+4:]
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return raw[:i], raw[i+2:]
	}
	return raw, nil
}

// canonicalHeaderKey mimics textproto.CanonicalMIMEHeaderKey for the keys
// produced by parseFieldGroup (all lower case).
func canonicalHeaderKey(k string) string {
	parts := strings.Split(k, "-")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "-")
}

// htmlToText renders an HTML body as plain text for the classifier and the
// extractor when a message has no usable text/plain part (design 5.2):
// comments, script, style and head are dropped, block-level tags become
// line breaks, table cells a space, every other tag disappears, character
// references (&nbsp; &amp; &lt; &#39; ...) are decoded and whitespace is
// collapsed. Blank lines are kept as paragraph breaks so that the body
// extraction rules see the same shape as a text part. "" when nothing but
// markup is left.
func htmlToText(markup string) string {
	if strings.TrimSpace(markup) == "" {
		return ""
	}
	s := htmlDropRe.ReplaceAllString(markup, " ")
	s = htmlBlockRe.ReplaceAllString(s, "\n")
	s = htmlCellRe.ReplaceAllString(s, " ")
	s = htmlTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\u00a0", " ")
	var out []string
	blank := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(multiSpaceRe.ReplaceAllString(line, " "))
		if line == "" {
			blank = len(out) > 0
			continue
		}
		if blank {
			out = append(out, "")
			blank = false
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

var (
	htmlDropRe  = regexp.MustCompile(`(?is)<!--.*?-->|<(script|style|head|title)\b[^>]*>.*?</(script|style|head|title)\s*>`)
	htmlBlockRe = regexp.MustCompile(`(?i)<\s*/?\s*(br|p|div|tr|li|ul|ol|h[1-6]|table|pre|blockquote|hr|section|article|header|footer|dt|dd)\b[^>]*>`)
	htmlCellRe  = regexp.MustCompile(`(?i)<\s*/?\s*(td|th)\b[^>]*>`)
	htmlTagRe   = regexp.MustCompile(`(?s)<[^>]*>`)
)

// bodyForClassification returns the primary body the classifier and the
// extractor look at first: every text section joined, else every HTML
// section rendered as text and joined, else "". secondaryBody is the other
// one (design 5.2: both bodies are consulted, the primary first).
func (pm *ParsedMessage) bodyForClassification() string {
	switch pm.primarySource() {
	case bodySourceText:
		return pm.textBodyJoined()
	case bodySourceHTML:
		return pm.htmlTextBody()
	}
	return ""
}

// secondaryBody returns the HTML sections rendered as text when the message
// has both text and HTML sections, "" otherwise.
func (pm *ParsedMessage) secondaryBody() string {
	if len(pm.TextSections) > 0 && len(pm.HTMLSections) > 0 {
		return pm.htmlTextBody()
	}
	return ""
}

// primarySource is the body source of the primary body ("text" when the
// message has text sections, else "html" when it has HTML sections, else "").
func (pm *ParsedMessage) primarySource() string {
	switch {
	case len(pm.TextSections) > 0:
		return bodySourceText
	case len(pm.HTMLSections) > 0:
		return bodySourceHTML
	}
	return ""
}

// textBodyJoined returns the text sections joined into one body (cached by
// finishBodies; computed here for a ParsedMessage built by hand).
func (pm *ParsedMessage) textBodyJoined() string {
	if pm.textBody == "" && len(pm.TextSections) > 0 {
		pm.textBody = joinSections(pm.TextSections, "\n\n")
	}
	return pm.textBody
}

// htmlTextBody returns the HTML sections rendered as text and joined
// (cached by finishBodies; computed here for a ParsedMessage built by hand).
func (pm *ParsedMessage) htmlTextBody() string {
	if pm.htmlText == "" && len(pm.HTMLSections) > 0 {
		texts := make([]string, 0, len(pm.HTMLSections))
		for _, s := range pm.HTMLSections {
			texts = append(texts, htmlToText(s))
		}
		pm.htmlText = joinSections(texts, "\n\n")
	}
	return pm.htmlText
}

package mailengine

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	netmail "net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // registers ISO-2022-JP, Shift_JIS, EUC-JP and friends
	"github.com/emersion/go-message/mail"
)

// ParsedMessage is the content of <message_key>.json: the headers and the
// structured parts the classifier and the Web UI need. The bodies are stored
// in the sibling .txt / .html files and are therefore not serialized.
type ParsedMessage struct {
	Source Source `json:"source"`

	MessageID     string    `json:"message_id"`
	Subject       string    `json:"subject"`
	FromAddress   string    `json:"from_address"`
	FromName      string    `json:"from_name"`
	To            string    `json:"to"`
	Date          time.Time `json:"date,omitzero"`
	ReturnPath    string    `json:"return_path"`
	AutoSubmitted string    `json:"auto_submitted"`
	ContentType   string    `json:"content_type"` // media type only, lower case
	ReportType    string    `json:"report_type"`  // report-type parameter of multipart/report
	// Headers holds every top-level header (canonical key, decoded, values
	// joined with a newline) for display.
	Headers map[string]string `json:"headers"`

	HasText bool `json:"has_text"`
	HasHTML bool `json:"has_html"`

	DeliveryStatus  *DeliveryStatus  `json:"delivery_status,omitempty"`
	OriginalMessage *OriginalMessage `json:"original_message,omitempty"`

	// ParseError records why the MIME parser gave up; the fields above hold
	// whatever could still be read.
	ParseError string `json:"parse_error,omitempty"`

	// TextBody / HTMLBody are the decoded bodies (UTF-8). They live in the
	// .txt / .html files and are only kept in memory.
	TextBody string `json:"-"`
	HTMLBody string `json:"-"`

	parts int // leaf parts seen by walk (not serialized)
}

// Source records where the message came from so that Reindex can rebuild the
// index rows without talking to the IMAP server again.
type Source struct {
	MessageKey  string    `json:"message_key"`
	Folder      string    `json:"folder"`
	UIDValidity uint32    `json:"uidvalidity"`
	UID         uint32    `json:"uid"`
	Size        int64     `json:"size"`
	ReceivedAt  time.Time `json:"received_at,omitzero"` // IMAP INTERNALDATE
	FetchedAt   time.Time `json:"fetched_at,omitzero"`
}

// DeliveryStatus is the structured message/delivery-status part (RFC 3464).
type DeliveryStatus struct {
	ReportingMTA string                    `json:"reporting_mta"`
	ArrivalDate  string                    `json:"arrival_date"`
	Recipients   []DeliveryStatusRecipient `json:"recipients"`
}

// DeliveryStatusRecipient is one per-recipient block of a delivery-status part.
type DeliveryStatusRecipient struct {
	FinalRecipient    string `json:"final_recipient"`
	OriginalRecipient string `json:"original_recipient"`
	Action            string `json:"action"`
	Status            string `json:"status"`
	DiagnosticCode    string `json:"diagnostic_code"`
	RemoteMTA         string `json:"remote_mta"`
}

// OriginalMessage holds the headers of the message a bounce refers to, taken
// from a message/rfc822 or text/rfc822-headers part.
type OriginalMessage struct {
	MessageID string    `json:"message_id"`
	Subject   string    `json:"subject"`
	From      string    `json:"from"`
	Date      time.Time `json:"date,omitzero"`
}

// Limits that keep a hostile or broken message from exhausting memory.
const (
	maxBodyBytes    = 4 * 1024 * 1024 // text kept per body kind
	maxHeaderValue  = 8 * 1024        // per header value in Headers
	maxNestingDepth = 8               // multipart / message nesting
)

// ParseMessage parses a raw RFC 5322 message. It never panics and never
// returns a nil message: on a broken message the returned ParsedMessage holds
// whatever headers could be read and ParseError describes the failure.
func ParseMessage(raw []byte) (pm *ParsedMessage) {
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
	if err := pm.walk(entity, 0, false); err != nil && pm.ParseError == "" {
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
	pm.HasText = pm.TextBody != ""
	pm.HasHTML = pm.HTMLBody != ""
	return pm
}

// readTopHeaders fills the header fields from the root entity.
func (pm *ParsedMessage) readTopHeaders(h message.Header) {
	mh := mail.Header{Header: h}
	pm.MessageID = headerMessageID(mh)
	pm.Subject = headerText(mh, "Subject")
	pm.FromAddress, pm.FromName = headerAddress(mh, "From")
	pm.To = headerAddressList(mh, "To")
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
		val = truncate(strings.TrimSpace(val), maxHeaderValue)
		if prev, ok := pm.Headers[key]; ok {
			pm.Headers[key] = prev + "\n" + val
		} else {
			pm.Headers[key] = val
		}
	}
}

// walk descends the MIME tree. inOriginal is true inside a message/rfc822
// part: only the headers of the embedded message are of interest there.
func (pm *ParsedMessage) walk(e *message.Entity, depth int, inOriginal bool) error {
	if depth > maxNestingDepth {
		return nil
	}
	ct, _, err := e.Header.ContentType()
	if err != nil {
		ct = "text/plain"
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
			if err := pm.walk(part, depth+1, inOriginal); err != nil {
				return err
			}
		}
	case ct == "message/delivery-status":
		if !inOriginal && pm.DeliveryStatus == nil {
			body, _ := readLimited(e.Body, maxBodyBytes)
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
		return pm.walk(inner, depth+1, true)
	case ct == "text/rfc822-headers":
		if inOriginal || pm.OriginalMessage != nil {
			_, _ = io.Copy(io.Discard, e.Body)
			return nil
		}
		body, _ := readLimited(e.Body, maxBodyBytes)
		hdr, err := message.Read(bytes.NewReader(append(bytes.TrimLeft(body, "\r\n"), "\r\n\r\n"...)))
		if hdr != nil && (err == nil || message.IsUnknownCharset(err) || message.IsUnknownEncoding(err)) {
			pm.OriginalMessage = originalFromHeader(hdr.Header)
		}
	case ct == "text/plain":
		if inOriginal || isAttachment || pm.TextBody != "" {
			_, _ = io.Copy(io.Discard, e.Body)
			return nil
		}
		body, _ := readLimited(e.Body, maxBodyBytes)
		pm.TextBody = normalizeText(body)
	case ct == "text/html":
		if inOriginal || isAttachment || pm.HTMLBody != "" {
			_, _ = io.Copy(io.Discard, e.Body)
			return nil
		}
		body, _ := readLimited(e.Body, maxBodyBytes)
		pm.HTMLBody = normalizeText(body)
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
			Action:            strings.ToLower(fields["action"]),
			Status:            strings.TrimSpace(fields["status"]),
			DiagnosticCode:    fields["diagnostic-code"],
			RemoteMTA:         stripTypePrefix(fields["remote-mta"]),
		})
	}
	if ds.ReportingMTA == "" && ds.ArrivalDate == "" && len(ds.Recipients) == 0 {
		return nil
	}
	return ds
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
	v, err := h.Text(key)
	if err != nil {
		v = decodeWordsLoose(h.Get(key))
	}
	return strings.TrimSpace(strings.Join(strings.Fields(v), " "))
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
	if list, err := h.AddressList(key); err == nil && len(list) > 0 {
		return strings.TrimSpace(list[0].Address), strings.TrimSpace(list[0].Name)
	}
	raw := decodeWordsLoose(h.Get(key))
	return looseAddress(raw)
}

// headerAddressList returns all addresses of an address header joined by ", ".
func headerAddressList(h mail.Header, key string) string {
	if list, err := h.AddressList(key); err == nil && len(list) > 0 {
		parts := make([]string, 0, len(list))
		for _, a := range list {
			parts = append(parts, formatAddress(a.Address, a.Name))
		}
		return strings.Join(parts, ", ")
	}
	raw := decodeWordsLoose(h.Get(key))
	addr, name := looseAddress(raw)
	if addr == "" && name == "" {
		return strings.TrimSpace(raw)
	}
	return formatAddress(addr, name)
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
	dec := mime.WordDecoder{CharsetReader: message.CharsetReader}
	if out, err := dec.DecodeHeader(s); err == nil {
		return out
	}
	return s
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

// readLimited reads at most limit bytes and drains the rest.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	b, err := io.ReadAll(io.LimitReader(r, limit))
	_, _ = io.Copy(io.Discard, r)
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return b, err
	}
	return b, nil
}

// normalizeText converts a decoded body to UTF-8 text with LF line endings.
// go-message already converted known charsets; unknown ones arrive as raw
// bytes and are made valid UTF-8 by replacing bad sequences.
func normalizeText(b []byte) string {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	s = strings.TrimPrefix(s, "\ufeff")
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\ufffd")
	}
	return s
}

// truncate cuts s to at most n bytes on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
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
		pm.To = get("to")
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
			pm.Headers[canonicalHeaderKey(k)] = truncate(decodeWordsLoose(v), maxHeaderValue)
		}
	}
	pm.fillFallbackBody(raw)
}

// fillFallbackBody keeps the bytes after the header block as the text body
// so that the classifier still has something to look at.
func (pm *ParsedMessage) fillFallbackBody(raw []byte) {
	if pm.TextBody != "" {
		return
	}
	_, body := splitHeaderBlock(raw)
	if len(body) == 0 {
		return
	}
	pm.TextBody = normalizeText(bytes.TrimLeft(body[:min(len(body), maxBodyBytes)], "\r\n"))
	pm.HasText = pm.TextBody != ""
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

// htmlToText produces a rough text rendering of an HTML body for the
// classifier when a message has no text/plain part. It is not saved.
func htmlToText(html string) string {
	s := htmlScriptStyleRe.ReplaceAllString(html, " ")
	s = htmlBlockRe.ReplaceAllString(s, "\n")
	s = htmlTagRe.ReplaceAllString(s, " ")
	s = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&apos;", "'").Replace(s)
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(multiSpaceRe.ReplaceAllString(line, " "))
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

var (
	htmlScriptStyleRe = regexp.MustCompile(`(?is)<(script|style|head)[^>]*>.*?</(script|style|head)>`)
	htmlBlockRe       = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/tr|/li|/h[1-6]|/table|/pre)\s*/?\s*>`)
	htmlTagRe         = regexp.MustCompile(`(?s)<[^>]*>`)
)

// bodyForClassification returns the text the classifier and the extractor
// look at: the text body, else a text rendering of the HTML body.
func (pm *ParsedMessage) bodyForClassification() string {
	if pm.TextBody != "" {
		return pm.TextBody
	}
	if pm.HTMLBody != "" {
		return htmlToText(pm.HTMLBody)
	}
	return ""
}

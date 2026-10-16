package modules

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mailcare/app/modules/wording"
	"mime"
	"net"
	netmail "net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// SMTP client for the notification mails (the system design document
// (Documents) 7.4). Only what MailCare needs: one connection per mail, the
// three security modes shared with IMAP (ssl = implicit TLS, starttls, none),
// PLAIN or LOGIN authentication, and a UTF-8 text/plain message.

// SMTPConfig is the resolved SMTP setting (the password already decrypted).
type SMTPConfig struct {
	Host     string
	Port     int
	Security string // IMAPSecuritySSL | IMAPSecurityStartTLS | IMAPSecurityNone
	Username string
	Password string
	From     string // sender address (the display name is always MailCare)
}

// SMTPFromName is the display name put in front of the sender address.
const SMTPFromName = "MailCare"

// ErrSMTPNotConfigured is returned when the host or the sender is missing.
var ErrSMTPNotConfigured = wording.New("result.notification.smtpNotConfigured", "SMTP host or sender address is not set")

// ValidateSMTPSecurity checks a security mode (the values are shared with IMAP).
func ValidateSMTPSecurity(s string) error {
	switch s {
	case IMAPSecuritySSL, IMAPSecurityStartTLS, IMAPSecurityNone:
		return nil
	}
	return wording.New("system.invalidRequest", fmt.Sprintf("smtp_security must be %s, %s or %s", IMAPSecuritySSL, IMAPSecurityStartTLS, IMAPSecurityNone))
}

// Configured reports whether the host and the sender address are set.
func (c SMTPConfig) Configured() bool {
	return strings.TrimSpace(c.Host) != "" && strings.TrimSpace(c.From) != ""
}

// MailHeader is an extra header line of an outgoing message (ASCII name
// and value).
type MailHeader struct {
	Name  string
	Value string
}

// OutgoingMail is the content of one outgoing message.
type OutgoingMail struct {
	FromName string // display name of the sender ("" = SMTPFromName)
	Subject  string
	Body     string // UTF-8 plain text
	Headers  []MailHeader
}

// SendMail delivers one UTF-8 text/plain mail through the configured server.
// from is the envelope and header sender, to the envelope and header
// recipients. Each step is bounded by its timeout of RFC 5321 4.5.3.2
// (smtpTimeouts); the session as a whole has no time limit (ctx still
// stops it).
func SendMail(ctx context.Context, cfg SMTPConfig, from string, to []string, subject, textBody string) error {
	return SendMessage(ctx, cfg, from, to, OutgoingMail{Subject: subject, Body: textBody})
}

// SendMessage is SendMail for a message with another display name or extra
// headers.
func SendMessage(ctx context.Context, cfg SMTPConfig, from string, to []string, mail OutgoingMail) error {
	if !cfg.Configured() {
		return ErrSMTPNotConfigured
	}
	if err := ValidateSMTPSecurity(cfg.Security); err != nil {
		return err
	}
	from = strings.TrimSpace(from)
	if from == "" {
		return errors.New("sender address is empty")
	}
	recipients := make([]string, 0, len(to))
	for _, r := range to {
		if r = strings.TrimSpace(r); r != "" {
			recipients = append(recipients, r)
		}
	}
	if len(recipients) == 0 {
		return errors.New("no recipient")
	}
	port := cfg.Port
	if port <= 0 {
		port = DefaultSMTPPort
	}
	host := strings.TrimSpace(cfg.Host)
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	// The connection is made within the connect timeout of the operating
	// system; a cancellation of ctx cuts it at any step.
	dialer := &net.Dialer{}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	conn := &smtpConn{Conn: raw, ctx: ctx}
	stop := context.AfterFunc(ctx, func() { _ = raw.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	// step gives the next command (sending it and reading its reply) its
	// timeout.
	step := func(d time.Duration) {
		_ = raw.SetDeadline(time.Now().Add(d))
		conn.keepCut()
	}
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	var c net.Conn = conn
	step(smtpTimeouts.greeting)
	if cfg.Security == IMAPSecuritySSL {
		tlsConn := tls.Client(conn, tlsConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return fmt.Errorf("TLS handshake with %s: %w", addr, ctxErr(ctx, err))
		}
		c = tlsConn
	}
	client, err := smtp.NewClient(c, host)
	if err != nil {
		raw.Close()
		return fmt.Errorf("SMTP greeting from %s: %w", addr, ctxErr(ctx, err))
	}
	defer client.Close()
	step(smtpTimeouts.command)
	if err := client.Hello(ehloName(from)); err != nil {
		return fmt.Errorf("EHLO: %w", ctxErr(ctx, err))
	}
	if cfg.Security == IMAPSecurityStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("the server does not offer STARTTLS")
		}
		step(smtpTimeouts.command)
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("STARTTLS: %w", ctxErr(ctx, err))
		}
	}
	if cfg.Username != "" {
		auth, err := chooseAuth(client, cfg.Username, cfg.Password)
		if err != nil {
			return err
		}
		step(smtpTimeouts.command)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("authentication failed: %w", ctxErr(ctx, err))
		}
	}
	step(smtpTimeouts.mail)
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM: %w", ctxErr(ctx, err))
	}
	for _, r := range recipients {
		step(smtpTimeouts.rcpt)
		if err := client.Rcpt(r); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", r, ctxErr(ctx, err))
		}
	}
	step(smtpTimeouts.dataInit)
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", ctxErr(ctx, err))
	}
	// Each write of the message must complete within the data block
	// timeout; the time of the whole message is not limited.
	conn.perWrite.Store(int64(smtpTimeouts.dataBlock))
	_, werr := w.Write([]byte(buildMessage(from, recipients, mail, time.Now())))
	conn.perWrite.Store(0)
	if werr != nil {
		w.Close()
		return fmt.Errorf("send message: %w", ctxErr(ctx, werr))
	}
	step(smtpTimeouts.dataEnd)
	if err := w.Close(); err != nil {
		return fmt.Errorf("send message: %w", ctxErr(ctx, err))
	}
	step(smtpTimeouts.command)
	if err := client.Quit(); err != nil {
		// The message was accepted; a failed QUIT is not worth reporting.
		return nil
	}
	return nil
}

// smtpTimeouts are the per-command timeouts of the client, from RFC 5321
// 4.5.3.2: the initial 220 greeting (and the TLS handshake before it on an
// implicit TLS connection) 5 minutes, MAIL 5 minutes, RCPT 5 minutes, DATA
// initiation 2 minutes, each write of the data block 3 minutes, DATA
// termination (the final "." until its reply) 10 minutes. EHLO, STARTTLS,
// AUTH and QUIT are not listed there; they get the 5 minutes of the other
// commands. A variable so that the tests can shorten them.
var smtpTimeouts = struct {
	greeting, command, mail, rcpt, dataInit, dataBlock, dataEnd time.Duration
}{
	greeting:  5 * time.Minute,
	command:   5 * time.Minute,
	mail:      5 * time.Minute,
	rcpt:      5 * time.Minute,
	dataInit:  2 * time.Minute,
	dataBlock: 3 * time.Minute,
	dataEnd:   10 * time.Minute,
}

// smtpConn is the connection of one session: while perWrite is set (the
// data block), each write gets that long to complete.
type smtpConn struct {
	net.Conn
	ctx      context.Context
	perWrite atomic.Int64 // time.Duration; 0 = the deadline of the step
}

func (c *smtpConn) Write(b []byte) (int, error) {
	if d := time.Duration(c.perWrite.Load()); d > 0 {
		if err := c.Conn.SetWriteDeadline(time.Now().Add(d)); err != nil {
			return 0, err
		}
		c.keepCut()
	}
	return c.Conn.Write(b)
}

// keepCut moves the deadline back to the past when ctx ended: a deadline
// set after the cancellation cut the connection must not reopen it.
func (c *smtpConn) keepCut() {
	if c.ctx.Err() != nil {
		_ = c.Conn.SetDeadline(time.Unix(1, 0))
	}
}

// ctxErr prefers the error of ctx when it ended (the transport error is
// then only the cut connection).
func ctxErr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return err
}

// ehloName derives the EHLO host name from the sender's domain.
func ehloName(from string) string {
	if _, domain, ok := strings.Cut(from, "@"); ok && domain != "" {
		return domain
	}
	return "localhost"
}

// chooseAuth picks PLAIN when the server offers it (or names no mechanism)
// and LOGIN when that is the only one offered.
func chooseAuth(client *smtp.Client, username, password string) (smtp.Auth, error) {
	ok, mechs := client.Extension("AUTH")
	if !ok {
		return nil, errors.New("the server does not offer authentication (remove the SMTP username, or use another server)")
	}
	upper := strings.ToUpper(mechs)
	fields := strings.Fields(upper)
	has := func(m string) bool {
		for _, f := range fields {
			if f == m {
				return true
			}
		}
		return false
	}
	switch {
	case has("PLAIN") || len(fields) == 0:
		return plainAuth{username: username, password: password}, nil
	case has("LOGIN"):
		return loginAuth{username: username, password: password}, nil
	}
	return nil, fmt.Errorf("the server offers no supported authentication mechanism (%s); PLAIN or LOGIN is required", mechs)
}

// plainAuth is SASL PLAIN. Unlike smtp.PlainAuth it does not refuse a
// plaintext connection: "none" is an explicit operator choice (a local
// relay), and the settings screen warns about it.
type plainAuth struct{ username, password string }

func (a plainAuth) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.username + "\x00" + a.password), nil
}

func (a plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge during PLAIN authentication")
	}
	return nil, nil
}

// loginAuth is the LOGIN mechanism (username and password answered to two
// challenges), offered by servers that do not support PLAIN.
type loginAuth struct{ username, password string }

func (a loginAuth) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	return "LOGIN", nil, nil
}

func (a loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:", "username":
		return []byte(a.username), nil
	case "password:", "password":
		return []byte(a.password), nil
	}
	return nil, fmt.Errorf("unexpected server challenge during LOGIN authentication: %q", fromServer)
}

// --- Message ---

// maxLineLength is the longest body line sent as 8bit (RFC 5322 allows 998).
const maxLineLength = 998

// BuildMessage renders the RFC 5322 message: Date, Message-ID, From (with the
// MailCare display name), To, Subject (RFC 2047 when needed), MIME-Version,
// Content-Type text/plain UTF-8 and the body as 8bit, or base64 when a line is
// too long for 8bit. Lines end with CRLF.
func BuildMessage(from string, to []string, subject, textBody string, now time.Time) string {
	return buildMessage(from, to, OutgoingMail{Subject: subject, Body: textBody}, now)
}

// buildMessage is BuildMessage with the display name and the extra headers
// of mail (header lines with a CR or LF in the name or value are dropped).
func buildMessage(from string, to []string, mail OutgoingMail, now time.Time) string {
	fromName := mail.FromName
	if fromName == "" {
		fromName = SMTPFromName
	}
	textBody := mail.Body
	var b strings.Builder
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: " + messageID(from, now) + "\r\n")
	b.WriteString(headerLine("From", formatAddress(fromName, from)))
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString(headerLine("Subject", encodeHeader(mail.Subject)))
	for _, h := range mail.Headers {
		if h.Name == "" || strings.ContainsAny(h.Name, ": \r\n") || strings.ContainsAny(h.Value, "\r\n") {
			continue
		}
		b.WriteString(h.Name + ": " + h.Value + "\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	body := strings.ReplaceAll(strings.ReplaceAll(textBody, "\r\n", "\n"), "\n", "\r\n")
	if needsBase64(body) {
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		b.WriteString(wrapBase64(body))
	} else {
		b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		b.WriteString(body)
	}
	if !strings.HasSuffix(b.String(), "\r\n") {
		b.WriteString("\r\n")
	}
	return b.String()
}

// formatAddress renders a display name and an address for the From header
// (net/mail: an ASCII name is quoted, so a comma, colon or @ in it keeps the
// header readable; other names become RFC 2047 encoded words). White space
// in the name is collapsed.
func formatAddress(name, address string) string {
	return (&netmail.Address{Name: strings.Join(strings.Fields(name), " "), Address: address}).String()
}

// headerLine renders one header line ending in CRLF. A line longer than the
// line limit (maxLineLength octets, such as a long encoded display name) is
// folded at its spaces into lines of about 78 characters; shorter lines are
// left as they are.
func headerLine(name, value string) string {
	line := name + ": " + value
	if len(line) <= maxLineLength {
		return line + "\r\n"
	}
	var b strings.Builder
	for len(line) > 78 {
		cut := strings.LastIndexByte(line[1:79], ' ') + 1
		if cut == 0 {
			next := strings.IndexByte(line[1:], ' ')
			if next < 0 {
				break
			}
			cut = next + 1
		}
		b.WriteString(line[:cut] + "\r\n")
		line = line[cut:] // the space starts the continuation line
	}
	b.WriteString(line + "\r\n")
	return b.String()
}

// encodeHeader encodes a header value as an RFC 2047 encoded word when it
// contains non-ASCII characters (ASCII passes through unchanged).
func encodeHeader(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return mime.QEncoding.Encode("utf-8", s)
}

// needsBase64 reports whether a body line exceeds the 8bit line limit or the
// body carries a NUL byte.
func needsBase64(body string) bool {
	for _, line := range strings.Split(body, "\r\n") {
		if len(line) > maxLineLength {
			return true
		}
	}
	return strings.ContainsRune(body, 0)
}

// wrapBase64 encodes the body and folds it into 76-character lines.
func wrapBase64(body string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.String()
}

// messageID builds "<random.time@domain>" from the sender's domain.
func messageID(from string, now time.Time) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		// Fall back to the clock only; uniqueness is best effort here.
		buf = []byte(strconv.FormatInt(now.UnixNano(), 10))
	}
	return fmt.Sprintf("<%s.%d@%s>", hex.EncodeToString(buf), now.Unix(), ehloName(from))
}

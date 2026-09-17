package modules

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
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
var ErrSMTPNotConfigured = errors.New("SMTP host or sender address is not set")

// ValidateSMTPSecurity checks a security mode (the values are shared with IMAP).
func ValidateSMTPSecurity(s string) error {
	switch s {
	case IMAPSecuritySSL, IMAPSecurityStartTLS, IMAPSecurityNone:
		return nil
	}
	return fmt.Errorf("smtp_security must be %s, %s or %s", IMAPSecuritySSL, IMAPSecurityStartTLS, IMAPSecurityNone)
}

// Configured reports whether the host and the sender address are set.
func (c SMTPConfig) Configured() bool {
	return strings.TrimSpace(c.Host) != "" && strings.TrimSpace(c.From) != ""
}

// SendMail delivers one UTF-8 text/plain mail through the configured server.
// from is the envelope and header sender, to the envelope and header
// recipients. The whole session is bounded by SMTPTimeout and by ctx.
func SendMail(ctx context.Context, cfg SMTPConfig, from string, to []string, subject, textBody string) error {
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

	ctx, cancel := context.WithTimeout(ctx, SMTPTimeout)
	defer cancel()
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if cfg.Security == IMAPSecuritySSL {
		tlsConn := tls.Client(conn, tlsConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return fmt.Errorf("TLS handshake with %s: %w", addr, err)
		}
		conn = tlsConn
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP greeting from %s: %w", addr, err)
	}
	defer client.Close()
	if err := client.Hello(ehloName(from)); err != nil {
		return fmt.Errorf("EHLO: %w", err)
	}
	if cfg.Security == IMAPSecurityStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("the server does not offer STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if cfg.Username != "" {
		auth, err := chooseAuth(client, cfg.Username, cfg.Password)
		if err != nil {
			return err
		}
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("authentication failed: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	for _, r := range recipients {
		if err := client.Rcpt(r); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", r, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write([]byte(BuildMessage(from, recipients, subject, textBody, time.Now()))); err != nil {
		w.Close()
		return fmt.Errorf("send message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	if err := client.Quit(); err != nil {
		// The message was accepted; a failed QUIT is not worth reporting.
		return nil
	}
	return nil
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
	var b strings.Builder
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: " + messageID(from, now) + "\r\n")
	b.WriteString("From: " + SMTPFromName + " <" + from + ">\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + encodeHeader(subject) + "\r\n")
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

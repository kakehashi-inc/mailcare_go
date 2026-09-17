package modules

import (
	"context"
	"mime"
	"os"
	"strings"
	"testing"
	"time"

	"mailcare/app/modules/smtptest"
)

// decodeSubject extracts and RFC 2047-decodes the Subject header.
func decodeSubject(t *testing.T, raw string) string {
	t.Helper()
	for _, line := range strings.Split(raw, "\r\n") {
		if v, ok := strings.CutPrefix(line, "Subject: "); ok {
			dec := new(mime.WordDecoder)
			s, err := dec.DecodeHeader(v)
			if err != nil {
				t.Fatalf("decode subject %q: %v", v, err)
			}
			return s
		}
	}
	t.Fatalf("no Subject header in:\n%s", raw)
	return ""
}

func startFakeSMTP(t *testing.T) *smtptest.Server {
	t.Helper()
	srv, err := smtptest.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv
}

func TestSendMailPlainAuthOverPlaintext(t *testing.T) {
	srv := startFakeSMTP(t)
	srv.Username, srv.Password = "bounce", "s3cret"
	cfg := SMTPConfig{Host: srv.Host(), Port: srv.Port(), Security: IMAPSecurityNone, Username: "bounce", Password: "s3cret", From: "mailcare@example.test"}
	body := "こんにちは\nline two\n.leading dot\n"
	err := SendMail(context.Background(), cfg, cfg.From, []string{"a@example.test", " b@example.test "}, "テスト Subject", body)
	if err != nil {
		t.Fatalf("SendMail: %v", err)
	}
	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("messages received: %d", len(msgs))
	}
	m := msgs[0]
	if m.AuthUser != "bounce" || m.AuthMech != "PLAIN" {
		t.Errorf("auth = %q via %q", m.AuthUser, m.AuthMech)
	}
	if m.From != "mailcare@example.test" || strings.Join(m.To, ",") != "a@example.test,b@example.test" {
		t.Errorf("envelope from %q to %v", m.From, m.To)
	}
	if got := decodeSubject(t, m.Data); got != "テスト Subject" {
		t.Errorf("subject = %q", got)
	}
	for _, want := range []string{"From: MailCare <mailcare@example.test>\r\n", "To: a@example.test, b@example.test\r\n",
		"MIME-Version: 1.0\r\n", "Content-Type: text/plain; charset=UTF-8\r\n", "Content-Transfer-Encoding: 8bit\r\n",
		"Date: ", "Message-ID: <", "@example.test>\r\n", "\r\n\r\nこんにちは\r\nline two\r\n.leading dot\r\n"} {
		if !strings.Contains(m.Data, want) {
			t.Errorf("message lacks %q:\n%s", want, m.Data)
		}
	}
	// A wrong password is reported as an authentication failure.
	cfg.Password = "wrong"
	if err := SendMail(context.Background(), cfg, cfg.From, []string{"a@example.test"}, "x", "y"); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("wrong password: %v", err)
	}
	if len(srv.Messages()) != 1 {
		t.Errorf("a message was accepted after a failed authentication")
	}
}

func TestSendMailLoginFallbackAndNoAuth(t *testing.T) {
	srv := startFakeSMTP(t)
	srv.Mechanisms = "LOGIN"
	srv.Username, srv.Password = "u", "p"
	cfg := SMTPConfig{Host: srv.Host(), Port: srv.Port(), Security: IMAPSecurityNone, Username: "u", Password: "p", From: "mc@example.test"}
	if err := SendMail(context.Background(), cfg, cfg.From, []string{"x@example.test"}, "s", "b"); err != nil {
		t.Fatalf("LOGIN: %v", err)
	}
	if msgs := srv.Messages(); len(msgs) != 1 || msgs[0].AuthMech != "LOGIN" || msgs[0].AuthUser != "u" {
		t.Errorf("messages %+v", msgs)
	}
	// Without a username no AUTH is attempted at all.
	cfg.Username, cfg.Password = "", ""
	if err := SendMail(context.Background(), cfg, cfg.From, []string{"x@example.test"}, "s", "b"); err != nil {
		t.Fatalf("no auth: %v", err)
	}
	if msgs := srv.Messages(); len(msgs) != 2 || msgs[1].AuthMech != "" {
		t.Errorf("messages %+v", msgs)
	}
	// STARTTLS against a server that does not offer it fails before any mail.
	cfg.Security = IMAPSecurityStartTLS
	if err := SendMail(context.Background(), cfg, cfg.From, []string{"x@example.test"}, "s", "b"); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("starttls: %v", err)
	}
	// Validation before any connection.
	for _, bad := range []SMTPConfig{{}, {Host: "h", From: ""}, {Host: "h", From: "f@x.test", Security: "tls"}} {
		if err := SendMail(context.Background(), bad, bad.From, []string{"x@example.test"}, "s", "b"); err == nil {
			t.Errorf("config %+v accepted", bad)
		}
	}
	if err := SendMail(context.Background(), cfg, cfg.From, nil, "s", "b"); err == nil || !strings.Contains(err.Error(), "no recipient") {
		t.Errorf("no recipient: %v", err)
	}
	// A closed port is a connection error.
	cfg.Security = IMAPSecurityNone
	cfg.Port = closedPort(t)
	if err := SendMail(context.Background(), cfg, cfg.From, []string{"x@example.test"}, "s", "b"); err == nil || !strings.Contains(err.Error(), "connect to") {
		t.Errorf("closed port: %v", err)
	}
}

func TestBuildMessageEncodings(t *testing.T) {
	now := time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)
	msg := BuildMessage("a@example.test", []string{"b@example.test"}, "Plain subject", "body\n", now)
	if !strings.Contains(msg, "Subject: Plain subject\r\n") {
		t.Errorf("ASCII subject was encoded:\n%s", msg)
	}
	if !strings.Contains(msg, "Date: "+now.Format(time.RFC1123Z)+"\r\n") {
		t.Errorf("date missing:\n%s", msg)
	}
	long := strings.Repeat("x", 1200)
	msg = BuildMessage("a@example.test", []string{"b@example.test"}, "s", long, now)
	if !strings.Contains(msg, "Content-Transfer-Encoding: base64\r\n") {
		t.Errorf("long line not base64:\n%s", msg)
	}
	for _, line := range strings.Split(msg, "\r\n") {
		if len(line) > 998 {
			t.Errorf("line longer than 998 bytes")
		}
	}
	if got := encodeHeader("日本語  題名"); !strings.HasPrefix(got, "=?utf-8?q?") {
		t.Errorf("encodeHeader = %q", got)
	}
}

func TestTestSMTPSendsTheTestTemplate(t *testing.T) {
	TemplatesFS = os.DirFS("../..")
	srv := startFakeSMTP(t)
	cfg := SMTPConfig{Host: srv.Host(), Port: srv.Port(), Security: IMAPSecurityNone, From: "mc@example.test"}
	if err := TestSMTP(context.Background(), cfg, "Admin@Example.test"); err != nil {
		t.Fatalf("TestSMTP: %v", err)
	}
	msgs := srv.Messages()
	if len(msgs) != 1 || msgs[0].To[0] != "admin@example.test" {
		t.Fatalf("messages %+v", msgs)
	}
	if got := decodeSubject(t, msgs[0].Data); !strings.HasPrefix(got, NotifyMailSubjectPrefix) {
		t.Errorf("subject = %q", got)
	}
	if !strings.Contains(msgs[0].Data, srv.Host()) || !strings.Contains(msgs[0].Data, "mc@example.test") {
		t.Errorf("test body lacks the settings:\n%s", msgs[0].Data)
	}
	if err := TestSMTP(context.Background(), cfg, ""); err == nil {
		t.Errorf("empty recipient accepted")
	}
	if err := TestSMTP(context.Background(), cfg, "not-an-address"); err == nil {
		t.Errorf("invalid recipient accepted")
	}
	if err := TestSMTP(context.Background(), SMTPConfig{}, "a@example.test"); err != ErrSMTPNotConfigured {
		t.Errorf("unconfigured: %v", err)
	}
}

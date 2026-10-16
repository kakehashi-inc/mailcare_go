package modules

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"mime"
	"net"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/testkit"
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

func startFakeSMTP(t *testing.T) *testkit.Server {
	t.Helper()
	srv, err := testkit.Start()
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
	for _, want := range []string{"From: \"MailCare\" <mailcare@example.test>\r\n", "To: a@example.test, b@example.test\r\n",
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
	srv := startFakeSMTP(t)
	cfg := SMTPConfig{Host: srv.Host(), Port: srv.Port(), Security: IMAPSecurityNone, From: "mc@example.test"}
	if err := TestSMTP(context.Background(), cfg, "Admin@Example.test", DefaultMailLocale()); err != nil {
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
	if err := TestSMTP(context.Background(), cfg, "", DefaultMailLocale()); err == nil {
		t.Errorf("empty recipient accepted")
	}
	if err := TestSMTP(context.Background(), cfg, "not-an-address", DefaultMailLocale()); err == nil {
		t.Errorf("invalid recipient accepted")
	}
	if err := TestSMTP(context.Background(), SMTPConfig{}, "a@example.test", DefaultMailLocale()); err != ErrSMTPNotConfigured {
		t.Errorf("unconfigured: %v", err)
	}
	// An English locale gets the English template; a language without
	// templates falls back to Japanese.
	en := MailLocale{Language: "en", Location: time.UTC}
	if err := TestSMTP(context.Background(), cfg, "b@example.test", en); err != nil {
		t.Fatal(err)
	}
	fr := MailLocale{Language: "fr", Location: time.UTC}
	if err := TestSMTP(context.Background(), cfg, "c@example.test", fr); err != nil {
		t.Fatal(err)
	}
	msgs = srv.Messages()
	if got := decodeSubject(t, msgs[len(msgs)-2].Data); !strings.HasPrefix(got, NotifyMailSubjectPrefix+"Test email (") {
		t.Errorf("english subject = %q", got)
	}
	if ja := decodeSubject(t, msgs[0].Data); decodeSubject(t, msgs[len(msgs)-1].Data)[:20] != ja[:20] {
		t.Errorf("an unknown language must fall back to the Japanese template")
	}
}

func TestMailLocale(t *testing.T) {
	if l := MailLocaleFor(nil); l.Language != "ja" || l.Location != time.Local {
		t.Errorf("nil user: %+v", l)
	}
	l := MailLocaleFor(&models.User{Language: "en", Timezone: "America/New_York"})
	if l.Language != "en" || l.Location.String() != "America/New_York" {
		t.Errorf("user locale: %+v", l)
	}
	if l := MailLocaleFor(&models.User{Language: "en", Timezone: "Not/AZone"}); l.Location != time.Local {
		t.Errorf("unknown zone must fall back to the server time: %+v", l)
	}
	if got := mailTemplatePath("notification", "en"); got != "templates/mail/notification_en.txt" {
		t.Errorf("english template = %q", got)
	}
	if got := mailTemplatePath("notification", "fr"); got != "templates/mail/notification_ja.txt" {
		t.Errorf("missing language must fall back to Japanese, got %q", got)
	}
	for _, lang := range []string{"ja", "en"} {
		labels, err := loadCategoryLabels(lang)
		if err != nil || len(labels) != len(KnownCategories()) {
			t.Errorf("%s category labels: %d (err %v)", lang, len(labels), err)
		}
	}
}

// shortSMTPTimeouts shortens every per-command timeout for a test.
func shortSMTPTimeouts(t *testing.T, d time.Duration) {
	t.Helper()
	saved := smtpTimeouts
	smtpTimeouts.greeting, smtpTimeouts.command, smtpTimeouts.mail, smtpTimeouts.rcpt = d, d, d, d
	smtpTimeouts.dataInit, smtpTimeouts.dataBlock, smtpTimeouts.dataEnd = d, d, d
	t.Cleanup(func() { smtpTimeouts = saved })
}

// TestSMTPTimeoutsOfRFC5321 checks the per-command timeouts against RFC 5321
// 4.5.3.2.
func TestSMTPTimeoutsOfRFC5321(t *testing.T) {
	for name, tc := range map[string]struct{ got, want time.Duration }{
		"greeting": {smtpTimeouts.greeting, 5 * time.Minute}, "mail": {smtpTimeouts.mail, 5 * time.Minute},
		"rcpt": {smtpTimeouts.rcpt, 5 * time.Minute}, "data initiation": {smtpTimeouts.dataInit, 2 * time.Minute},
		"data block": {smtpTimeouts.dataBlock, 3 * time.Minute}, "data termination": {smtpTimeouts.dataEnd, 10 * time.Minute},
		"other commands": {smtpTimeouts.command, 5 * time.Minute},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: %v, want %v", name, tc.got, tc.want)
		}
	}
}

// TestSendMailPerCommandTimeouts checks that each command has a timeout of
// its reply and the session none: a slow server whose every reply comes
// within the timeout of its command delivers the mail although the whole
// session takes many times that long, while a server that does not answer
// one command fails at that command after its timeout.
func TestSendMailPerCommandTimeouts(t *testing.T) {
	const timeout = 400 * time.Millisecond
	shortSMTPTimeouts(t, timeout)
	srv := startFakeSMTP(t)
	srv.SetDelay(100 * time.Millisecond) // per reply line: EHLO (three lines) stays within the timeout
	cfg := SMTPConfig{Host: srv.Host(), Port: srv.Port(), Security: IMAPSecurityNone, From: "mailcare@example.test"}
	var to []string
	for i := range 10 {
		to = append(to, fmt.Sprintf("r%d@example.test", i))
	}
	start := time.Now()
	if err := SendMail(context.Background(), cfg, cfg.From, to, "slow", "body"); err != nil {
		t.Fatalf("slow server: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 3*timeout {
		t.Errorf("the slow session took %v, want longer than several command timeouts", elapsed)
	}
	if len(srv.Messages()) != 1 {
		t.Fatalf("messages %d", len(srv.Messages()))
	}
	for stallOn, want := range map[string]string{"MAIL": "MAIL FROM", "RCPT": "RCPT TO", "DATA": "DATA", ".": "send message"} {
		srv := startFakeSMTP(t)
		srv.SetStallOn(stallOn)
		cfg := SMTPConfig{Host: srv.Host(), Port: srv.Port(), Security: IMAPSecurityNone, From: "mailcare@example.test"}
		start := time.Now()
		err := SendMail(context.Background(), cfg, cfg.From, []string{"a@example.test"}, "x", "y")
		elapsed := time.Since(start)
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("stalled on %s: %v, want an error of %s", stallOn, err, want)
		}
		if elapsed < timeout || elapsed > 10*timeout {
			t.Errorf("stalled on %s: gave up after %v, want the command timeout %v", stallOn, elapsed, timeout)
		}
	}
}

// TestSendMailCanceled checks that a cancellation stops a stalled session.
func TestSendMailCanceled(t *testing.T) {
	srv := startFakeSMTP(t)
	srv.SetStallOn("MAIL")
	cfg := SMTPConfig{Host: srv.Host(), Port: srv.Port(), Security: IMAPSecurityNone, From: "mailcare@example.test"}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	if err := SendMail(ctx, cfg, cfg.From, []string{"a@example.test"}, "x", "y"); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled session: %v", err)
	}
}

// TestSendMailTimeoutOfEachCommand checks that each command waits for its
// reply as long as the timeout of that command in RFC 5321 4.5.3.2 allows:
// with that timeout long and every other one short, a server that does not
// answer the command fails it only after the long one.
func TestSendMailTimeoutOfEachCommand(t *testing.T) {
	const short, long = 150 * time.Millisecond, 900 * time.Millisecond
	for _, tc := range []struct {
		stallOn, want string
		timeout       *time.Duration
	}{
		{"EHLO", "EHLO", &smtpTimeouts.command},
		{"MAIL", "MAIL FROM", &smtpTimeouts.mail},
		{"RCPT", "RCPT TO", &smtpTimeouts.rcpt},
		{"DATA", "DATA", &smtpTimeouts.dataInit},
		{".", "send message", &smtpTimeouts.dataEnd},
	} {
		t.Run(tc.stallOn, func(t *testing.T) {
			shortSMTPTimeouts(t, short)
			*tc.timeout = long
			srv := startFakeSMTP(t)
			srv.SetStallOn(tc.stallOn)
			cfg := SMTPConfig{Host: srv.Host(), Port: srv.Port(), Security: IMAPSecurityNone, From: "mailcare@example.test"}
			start := time.Now()
			err := SendMail(context.Background(), cfg, cfg.From, []string{"a@example.test"}, "x", "y")
			elapsed := time.Since(start)
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Errorf("stalled on %s: %v, want an error of %s", tc.stallOn, err, tc.want)
			}
			if elapsed < long*3/4 || elapsed > long+3*time.Second {
				t.Errorf("stalled on %s: gave up after %v, want the timeout of the command %v", tc.stallOn, elapsed, long)
			}
		})
	}
}

// TestSendMailGreetingTimeout checks that a server that accepts the
// connection and never greets fails the session after the greeting timeout.
func TestSendMailGreetingTimeout(t *testing.T) {
	const short, long = 150 * time.Millisecond, 900 * time.Millisecond
	shortSMTPTimeouts(t, short)
	smtpTimeouts.greeting = long
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		if conn, err := ln.Accept(); err == nil {
			accepted <- conn
		}
	}()
	t.Cleanup(func() {
		select {
		case conn := <-accepted:
			conn.Close()
		default:
		}
	})
	cfg := SMTPConfig{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Security: IMAPSecurityNone, From: "mailcare@example.test"}
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- SendMail(context.Background(), cfg, cfg.From, []string{"a@example.test"}, "x", "y") }()
	select {
	case err := <-done:
		elapsed := time.Since(start)
		if err == nil || !strings.HasPrefix(err.Error(), "SMTP greeting") {
			t.Errorf("silent server: %v, want an error of the greeting", err)
		}
		if elapsed < long*3/4 || elapsed > long+3*time.Second {
			t.Errorf("silent server: gave up after %v, want the greeting timeout %v", elapsed, long)
		}
	case <-time.After(long + 5*time.Second):
		t.Fatal("the session waited for the greeting without a time limit")
	}
}

// TestSendMailDataBlockPerWrite checks that the data block timeout bounds
// each write of the message, not the whole message: a server that reads a
// large message slowly, but never stalls a write for the timeout, receives
// it although the transfer takes several times that long.
func TestSendMailDataBlockPerWrite(t *testing.T) {
	const timeout = 600 * time.Millisecond
	shortSMTPTimeouts(t, timeout)
	smtpTimeouts.dataEnd = 10 * time.Second // the server still reads what the buffers hold after the last write
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	received := make(chan int, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// A fixed receive window: the message cannot sit in the buffers.
		_ = conn.(*net.TCPConn).SetReadBuffer(256 << 10)
		slowSMTPSink(conn, received)
	}()
	body := strings.Repeat(strings.Repeat("x", 76)+"\n", 16<<20/77)
	cfg := SMTPConfig{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Security: IMAPSecurityNone, From: "mailcare@example.test"}
	start := time.Now()
	if err := SendMail(context.Background(), cfg, cfg.From, []string{"a@example.test"}, "large", body); err != nil {
		t.Fatalf("slowly read message: %v after %v", err, time.Since(start))
	}
	if elapsed := time.Since(start); elapsed < 3*timeout {
		t.Errorf("the transfer took %v, want longer than several data block timeouts", elapsed)
	}
	select {
	case n := <-received:
		if n < len(body) {
			t.Errorf("the server received %d bytes, want the whole message (%d bytes of body)", n, len(body))
		}
	case <-time.After(5 * time.Second):
		t.Error("the server did not receive the message")
	}
}

// slowSMTPSink serves one SMTP session that accepts every command and reads
// the data block in pieces of at most 256 KiB, pausing 30 ms after each
// (a blocked write waits well within its timeout, the whole message far
// beyond it), and sends the size of the data block on received.
func slowSMTPSink(conn net.Conn, received chan<- int) {
	r := bufio.NewReader(conn)
	reply := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	reply("220 slow ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			reply("250 slow")
		case cmd == "DATA":
			reply("354 go ahead")
			buf := make([]byte, 256<<10)
			var tail []byte
			n := 0
			for !strings.HasSuffix(string(tail), "\r\n.\r\n") {
				k, err := r.Read(buf)
				if err != nil {
					return
				}
				n += k
				tail = append(tail, buf[:k]...)
				if len(tail) > 5 {
					tail = tail[len(tail)-5:]
				}
				time.Sleep(30 * time.Millisecond)
			}
			received <- n
			reply("250 2.0.0 queued")
		case cmd == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

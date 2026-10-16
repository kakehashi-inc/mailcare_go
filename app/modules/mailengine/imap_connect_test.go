package mailengine

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
)

// startScriptedIMAP serves one plaintext IMAP connection: the greeting
// (unless greet is false), CAPABILITY, STARTTLS (answered OK, then nothing:
// no TLS follows) and a LOGIN that onLogin answers (or not) through w. It
// returns the mailbox row that points at it.
func startScriptedIMAP(t *testing.T, greet bool, onLogin func(w *bufio.Writer, tag string)) *models.Mailbox {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r, w := bufio.NewReader(conn), bufio.NewWriter(conn)
		send := func(line string) { _, _ = w.WriteString(line + "\r\n"); _ = w.Flush() }
		if greet {
			send("* OK [CAPABILITY IMAP4rev1 STARTTLS] ready")
		}
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			tag, cmd := fields[0], strings.ToUpper(fields[1])
			switch cmd {
			case "CAPABILITY":
				send("* CAPABILITY IMAP4rev1 STARTTLS")
				send(tag + " OK done")
			case "STARTTLS":
				send(tag + " OK begin TLS")
				_, _ = io.Copy(io.Discard, r) // no TLS follows
				return
			case "LOGIN":
				onLogin(w, tag)
			case "LOGOUT":
				send("* BYE")
				send(tag + " OK done")
				return
			default:
				send(tag + " BAD unknown")
			}
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return &models.Mailbox{ID: 1, Address: memUsername, ImapHost: addr.IP.String(), ImapPort: addr.Port,
		ImapSecurity: imapSecurityNone, ImapUsername: memUsername, Folder: memFolder}
}

// TestIMAPConnectTimeouts checks the values of the connection phase.
func TestIMAPConnectTimeouts(t *testing.T) {
	if IMAPConnectTimeout != 30*time.Second || IMAPTLSHandshakeTimeout != 10*time.Second {
		t.Errorf("connect %v, TLS handshake %v; want 30 s and 10 s", IMAPConnectTimeout, IMAPTLSHandshakeTimeout)
	}
}

// TestIMAPGreetingInConnectionPhase checks that a server that never greets
// is given up after the connection phase, and that once connected a reply
// that takes far longer than that is waited for: no command has a time
// limit.
func TestIMAPGreetingInConnectionPhase(t *testing.T) {
	saved := imapConnectTimeout
	t.Cleanup(func() { imapConnectTimeout = saved })
	imapConnectTimeout = 300 * time.Millisecond

	silent := startScriptedIMAP(t, false, func(*bufio.Writer, string) {})
	start := time.Now()
	s, err := connect(context.Background(), silent, memPassword)
	if err == nil {
		s.close(false)
		t.Fatal("a server without a greeting was connected")
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) && !strings.Contains(err.Error(), "timeout") {
		t.Errorf("no greeting: %v, want a timeout", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("gave up after %v", d)
	}

	// The LOGIN reply comes after 1.5 s of silence, five times the
	// connection phase.
	slow := startScriptedIMAP(t, true, func(w *bufio.Writer, tag string) {
		time.Sleep(1500 * time.Millisecond)
		_, _ = w.WriteString(tag + " OK logged in\r\n")
		_ = w.Flush()
	})
	start = time.Now()
	s, err = connect(context.Background(), slow, memPassword)
	if err != nil {
		t.Fatalf("slow LOGIN: %v", err)
	}
	s.close(true)
	if d := time.Since(start); d < 1400*time.Millisecond {
		t.Errorf("slow LOGIN took %v, want the whole 1.5 s", d)
	}
}

// TestIMAPCancelDuringStartTLS checks that a cancellation closes the
// connection at once while the TLS handshake of STARTTLS waits for a
// server that does not answer it.
func TestIMAPCancelDuringStartTLS(t *testing.T) {
	mb := startScriptedIMAP(t, true, func(*bufio.Writer, string) {})
	mb.ImapSecurity = imapSecurityStartTLS
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	s, err := connect(ctx, mb, memPassword)
	if err == nil {
		s.close(false)
		t.Fatal("STARTTLS without TLS succeeded")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("canceled STARTTLS: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("returned after %v", d)
	}
}

// TestKeptDeadlines checks that the deadlines the IMAP client sets itself
// (30 s per response in go-imap) do not reach the connection.
func TestKeptDeadlines(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	k := keptDeadlines{a}
	if err := k.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := k.SetDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = b.Write([]byte("x")) }()
	buf := make([]byte, 1)
	if n, err := k.Read(buf); err != nil || n != 1 {
		t.Errorf("read after a past deadline: %d, %v; want the byte", n, err)
	}
}

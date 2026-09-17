// Package smtptest is a minimal fake SMTP server for tests: it listens on a
// loopback port, speaks EHLO / AUTH PLAIN / AUTH LOGIN / MAIL / RCPT / DATA /
// RSET / NOOP / QUIT over plaintext and records every accepted message. It
// is used by the tests of app/modules and app/workers; it is not part of the
// application.
package smtptest

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
)

// Message is one accepted mail.
type Message struct {
	From     string
	To       []string
	Data     string // the raw message as received (CRLF, dot-unstuffed)
	AuthUser string // the authenticated username ("" when no AUTH happened)
	AuthMech string // PLAIN or LOGIN
}

// Server is a running fake SMTP server.
type Server struct {
	// Username / Password are the credentials AUTH accepts; both empty
	// accepts any AUTH attempt.
	Username, Password string
	// Mechanisms is the AUTH line advertised by EHLO ("PLAIN LOGIN" by
	// default). Empty advertises no AUTH extension.
	Mechanisms string

	ln       net.Listener
	mu       sync.Mutex
	messages []Message
	wg       sync.WaitGroup
	closed   bool
}

// Start listens on 127.0.0.1:0 and serves until Close.
func Start() (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, Mechanisms: "PLAIN LOGIN"}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// Host / Port are the listening address.
func (s *Server) Host() string { return "127.0.0.1" }
func (s *Server) Port() int    { return s.ln.Addr().(*net.TCPAddr).Port }

// Close stops the listener and waits for the connections to finish.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.ln.Close()
	s.wg.Wait()
}

// Messages returns the accepted mails in order.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.messages...)
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

func (s *Server) accepts(user, pass string) bool {
	if s.Username == "" && s.Password == "" {
		return true
	}
	return user == s.Username && pass == s.Password
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	reply := func(line string) {
		_, _ = w.WriteString(line + "\r\n")
		_ = w.Flush()
	}
	readLine := func() (string, bool) {
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return "", false
		}
		return strings.TrimRight(line, "\r\n"), true
	}
	reply("220 smtptest ESMTP")
	var msg Message
	authed := ""
	authMech := ""
	for {
		line, ok := readLine()
		if !ok {
			return
		}
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			if s.Mechanisms != "" {
				reply("250-smtptest")
				reply("250-AUTH " + s.Mechanisms)
				reply("250 8BITMIME")
			} else {
				reply("250-smtptest")
				reply("250 8BITMIME")
			}
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			arg := strings.TrimSpace(line[len("AUTH PLAIN"):])
			if arg == "" {
				reply("334 ")
				if arg, ok = readLine(); !ok {
					return
				}
			}
			raw, err := base64.StdEncoding.DecodeString(arg)
			parts := strings.Split(string(raw), "\x00")
			if err != nil || len(parts) != 3 || !s.accepts(parts[1], parts[2]) {
				reply("535 5.7.8 authentication failed")
				continue
			}
			authed, authMech = parts[1], "PLAIN"
			reply("235 2.7.0 ok")
		case strings.HasPrefix(cmd, "AUTH LOGIN"):
			reply("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
			u, ok := readLine()
			if !ok {
				return
			}
			reply("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
			p, ok := readLine()
			if !ok {
				return
			}
			ub, err1 := base64.StdEncoding.DecodeString(u)
			pb, err2 := base64.StdEncoding.DecodeString(p)
			if err1 != nil || err2 != nil || !s.accepts(string(ub), string(pb)) {
				reply("535 5.7.8 authentication failed")
				continue
			}
			authed, authMech = string(ub), "LOGIN"
			reply("235 2.7.0 ok")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			msg = Message{From: angle(line[len("MAIL FROM:"):]), AuthUser: authed, AuthMech: authMech}
			reply("250 2.1.0 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			msg.To = append(msg.To, angle(line[len("RCPT TO:"):]))
			reply("250 2.1.5 ok")
		case cmd == "DATA":
			reply("354 end data with <CR><LF>.<CR><LF>")
			var b strings.Builder
			for {
				l, ok := readLine()
				if !ok {
					return
				}
				if l == "." {
					break
				}
				b.WriteString(strings.TrimPrefix(l, ".") + "\r\n")
			}
			msg.Data = b.String()
			s.mu.Lock()
			s.messages = append(s.messages, msg)
			n := len(s.messages)
			s.mu.Unlock()
			msg = Message{AuthUser: authed, AuthMech: authMech}
			reply(fmt.Sprintf("250 2.0.0 queued as %s", strconv.Itoa(n)))
		case cmd == "RSET":
			msg = Message{AuthUser: authed, AuthMech: authMech}
			reply("250 2.0.0 ok")
		case cmd == "NOOP":
			reply("250 2.0.0 ok")
		case cmd == "QUIT":
			reply("221 2.0.0 bye")
			return
		case strings.HasPrefix(cmd, "STARTTLS"):
			reply("454 4.7.0 TLS not available")
		default:
			reply("500 5.5.1 command not recognized")
		}
	}
}

// angle extracts the address of "<addr> ..." (or a bare address).
func angle(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "<"); i >= 0 {
		if j := strings.Index(s[i:], ">"); j > 0 {
			return s[i+1 : i+j]
		}
	}
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return s
}

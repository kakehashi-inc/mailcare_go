package dnscheck

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// A DNS server for the tests: UDP and TCP on a loopback address, answering
// each question from a table keyed by "<name> <TYPE>". Questions with the
// recursion-desired flag are answered from the resolver table, the others
// (the authoritative queries) from the authoritative table, so one server
// can play both roles.

// fakeMode is how a fake server answers one question.
type fakeMode int

const (
	fakeData          fakeMode = iota // the values as records
	fakeNX                            // NXDOMAIN
	fakeNoData                        // NOERROR without records
	fakeServFail                      // SERVFAIL
	fakeRefused                       // REFUSED
	fakeDrop                          // no reply
	fakeTruncate                      // over UDP a truncated reply without records, over TCP the data
	fakeWrongID                       // over UDP first an NXDOMAIN reply with another ID, then the data
	fakeWrongQuestion                 // over UDP first an NXDOMAIN reply to another question, then the data
	fakeNotAuth                       // the data without the authoritative flag
)

// fakeAnswer is the answer to one question: values are host names (MX in
// order of preference, NS), addresses (A, AAAA) or the text of a TXT record
// (one record per value, split into character-strings at each "|");
// "cname:<target>" adds a CNAME record instead.
type fakeAnswer struct {
	mode   fakeMode
	values []string
}

func data(values ...string) fakeAnswer { return fakeAnswer{mode: fakeData, values: values} }

func answerWith(mode fakeMode, values ...string) fakeAnswer {
	return fakeAnswer{mode: mode, values: values}
}

// fakeServer is one fake DNS server.
type fakeServer struct {
	ip   string
	port int
	udp  net.PacketConn
	tcp  net.Listener
	wg   sync.WaitGroup

	mu            sync.Mutex
	resolver      map[string]fakeAnswer // questions with recursion desired
	authoritative map[string]fakeAnswer // questions without (nil: REFUSED)
	queries       []string              // "rd|auth <name> <TYPE> udp|tcp" per question received
	raw           [][]byte              // the messages of those questions, as received
	conns         []net.Conn
	hook          func(query string) // runs for every question received, before the reply
}

// controlAddress is the answer of the fake resolvers to the control query.
const controlAddress = "192.0.2.80"

// startFakeServers starts one fake server per loopback address, all on the
// same port (Config.Port is one value for every resolver and authoritative
// server). Only Linux routes the whole of 127.0.0.0/8 to the loopback
// interface by default: elsewhere the test is skipped when an address other
// than 127.0.0.1 cannot be bound; on Linux that is a failure.
func startFakeServers(t *testing.T, ips ...string) []*fakeServer {
	t.Helper()
	for _, ip := range ips[1:] {
		probe, err := net.ListenPacket("udp", net.JoinHostPort(ip, "0"))
		if err != nil {
			if runtime.GOOS == "linux" {
				t.Fatalf("loopback address %s is not available: %v", ip, err)
			}
			t.Logf("skipped on %s: loopback address %s cannot be bound (%v)", runtime.GOOS, ip, err)
			t.Skipf("loopback address %s is not available: %v", ip, err)
		}
		probe.Close()
	}
	for attempt := 0; attempt < 20; attempt++ {
		servers, err := listenFakeServers(ips)
		if err != nil {
			continue
		}
		for _, s := range servers {
			s.wg.Go(s.serveUDP)
			s.wg.Go(s.serveTCP)
		}
		t.Cleanup(func() {
			for _, s := range servers {
				s.stop()
			}
		})
		return servers
	}
	t.Fatal("could not bind the fake DNS servers")
	return nil
}

// listenFakeServers binds UDP and TCP on the same free port of every
// address (an error when the port is taken on one of them).
func listenFakeServers(ips []string) ([]*fakeServer, error) {
	first, err := net.ListenPacket("udp", net.JoinHostPort(ips[0], "0"))
	if err != nil {
		return nil, err
	}
	port := first.LocalAddr().(*net.UDPAddr).Port
	var servers []*fakeServer
	fail := func(err error) ([]*fakeServer, error) {
		first.Close()
		for _, s := range servers {
			if s.udp != first {
				s.udp.Close()
			}
			s.tcp.Close()
		}
		return nil, err
	}
	for i, ip := range ips {
		udp := first
		addr := net.JoinHostPort(ip, strconv.Itoa(port))
		if i > 0 {
			if udp, err = net.ListenPacket("udp", addr); err != nil {
				return fail(err)
			}
		}
		tcp, err := net.Listen("tcp", addr)
		if err != nil {
			if udp != first {
				udp.Close()
			}
			return fail(err)
		}
		servers = append(servers, &fakeServer{ip: ip, port: port, udp: udp, tcp: tcp,
			resolver: map[string]fakeAnswer{controlName + " A": data(controlAddress)}})
	}
	return servers, nil
}

// stop closes the sockets and waits for the serving goroutines.
func (s *fakeServer) stop() {
	s.udp.Close()
	s.tcp.Close()
	s.mu.Lock()
	for _, c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// resolve sets the answer of the resolver role to a question ("name TYPE").
func (s *fakeServer) resolve(key string, a fakeAnswer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolver[key] = a
}

// authority sets the answer of the authoritative role to a question; it
// makes the server authoritative (an empty table answers NXDOMAIN).
func (s *fakeServer) authority(key string, a fakeAnswer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authoritative == nil {
		s.authoritative = map[string]fakeAnswer{}
	}
	if key != "" {
		s.authoritative[key] = a
	}
}

// onQuery sets the hook run for every question received (in the format of
// received), before the server replies.
func (s *fakeServer) onQuery(fn func(query string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hook = fn
}

// received returns the questions received so far.
func (s *fakeServer) received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

// rawReceived returns the messages of the questions received so far.
func (s *fakeServer) rawReceived() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.raw...)
}

// receivedFor returns the questions received about a name.
func (s *fakeServer) receivedFor(name string) []string {
	var out []string
	for _, q := range s.received() {
		if strings.Fields(q)[1] == name {
			out = append(out, q)
		}
	}
	return out
}

func (s *fakeServer) serveUDP() {
	buf := make([]byte, 65535)
	for {
		n, addr, err := s.udp.ReadFrom(buf)
		if err != nil {
			return
		}
		for _, r := range s.handle(buf[:n], false) {
			_, _ = s.udp.WriteTo(r, addr)
		}
	}
}

func (s *fakeServer) serveTCP() {
	for {
		conn, err := s.tcp.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.mu.Unlock()
		s.wg.Go(func() { s.serveConn(conn) })
	}
}

func (s *fakeServer) serveConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	for {
		var size [2]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			return
		}
		req := make([]byte, binary.BigEndian.Uint16(size[:]))
		if _, err := io.ReadFull(conn, req); err != nil {
			return
		}
		replies := s.handle(req, true)
		if len(replies) == 0 {
			continue
		}
		r := replies[len(replies)-1]
		frame := make([]byte, 2+len(r))
		binary.BigEndian.PutUint16(frame, uint16(len(r)))
		copy(frame[2:], r)
		if _, err := conn.Write(frame); err != nil {
			return
		}
	}
}

// handle answers one query: the replies to send, in order (none to drop).
func (s *fakeServer) handle(req []byte, tcp bool) [][]byte {
	var p dnsmessage.Parser
	h, err := p.Start(req)
	if err != nil {
		return nil
	}
	q, err := p.Question()
	if err != nil {
		return nil
	}
	name := strings.TrimSuffix(strings.ToLower(q.Name.String()), ".")
	rd := h.RecursionDesired
	role, proto := "auth", "udp"
	if rd {
		role = "rd"
	}
	if tcp {
		proto = "tcp"
	}
	query := role + " " + name + " " + typeName(q.Type) + " " + proto
	s.mu.Lock()
	s.queries = append(s.queries, query)
	s.raw = append(s.raw, append([]byte(nil), req...))
	a := s.answerFor(rd, name, q.Type)
	hook := s.hook
	s.mu.Unlock()
	if hook != nil {
		hook(query)
	}

	r := fakeReply{id: h.ID, q: q, rd: rd, aa: !rd}
	switch a.mode {
	case fakeDrop:
		return nil
	case fakeNX:
		r.rcode = dnsmessage.RCodeNameError
		return [][]byte{r.pack()}
	case fakeNoData:
		return [][]byte{r.pack()}
	case fakeServFail:
		r.rcode = dnsmessage.RCodeServerFailure
		r.aa = false
		return [][]byte{r.pack()}
	case fakeRefused:
		r.rcode = dnsmessage.RCodeRefused
		r.aa = false
		return [][]byte{r.pack()}
	case fakeNotAuth:
		r.aa = false
	case fakeTruncate:
		if !tcp {
			r.tc = true
			return [][]byte{r.pack()}
		}
	case fakeWrongID, fakeWrongQuestion:
		if !tcp {
			decoy := r
			decoy.rcode = dnsmessage.RCodeNameError
			if a.mode == fakeWrongID {
				decoy.id++
			} else {
				decoy.q.Name = dnsmessage.MustNewName("decoy.example.org.")
			}
			r.values = a.values
			return [][]byte{decoy.pack(), r.pack()}
		}
	}
	r.values = a.values
	return [][]byte{r.pack()}
}

// answerFor looks a question up in the table of the role: a missing
// question is NODATA when the table holds the name with another type and
// NXDOMAIN otherwise; a server without an authoritative table refuses
// authoritative queries.
func (s *fakeServer) answerFor(rd bool, name string, t dnsmessage.Type) fakeAnswer {
	table := s.resolver
	if !rd {
		table = s.authoritative
	}
	if table == nil {
		return fakeAnswer{mode: fakeRefused}
	}
	if a, ok := table[name+" "+typeName(t)]; ok {
		return a
	}
	for key := range table {
		if strings.HasPrefix(key, name+" ") {
			return fakeAnswer{mode: fakeNoData}
		}
	}
	return fakeAnswer{mode: fakeNX}
}

// fakeReply is a reply to build.
type fakeReply struct {
	id         uint16
	q          dnsmessage.Question
	rd, aa, tc bool
	rcode      dnsmessage.RCode
	values     []string
}

func (r fakeReply) pack() []byte {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: r.id, Response: true, Authoritative: r.aa, Truncated: r.tc,
		RecursionDesired: r.rd, RecursionAvailable: r.rd, RCode: r.rcode})
	must(b.StartQuestions())
	must(b.Question(r.q))
	must(b.StartAnswers())
	for i, v := range r.values {
		h := dnsmessage.ResourceHeader{Name: r.q.Name, Class: dnsmessage.ClassINET, TTL: 60}
		if target, ok := strings.CutPrefix(v, "cname:"); ok {
			must(b.CNAMEResource(h, dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName(target + ".")}))
			continue
		}
		switch r.q.Type {
		case dnsmessage.TypeMX:
			must(b.MXResource(h, dnsmessage.MXResource{Pref: uint16(10 * (i + 1)), MX: dnsmessage.MustNewName(v + ".")}))
		case dnsmessage.TypeNS:
			must(b.NSResource(h, dnsmessage.NSResource{NS: dnsmessage.MustNewName(v + ".")}))
		case dnsmessage.TypeA:
			must(b.AResource(h, dnsmessage.AResource{A: netip.MustParseAddr(v).As4()}))
		case dnsmessage.TypeAAAA:
			must(b.AAAAResource(h, dnsmessage.AAAAResource{AAAA: netip.MustParseAddr(v).As16()}))
		case dnsmessage.TypeTXT:
			must(b.TXTResource(h, dnsmessage.TXTResource{TXT: strings.Split(v, "|")}))
		}
	}
	msg, err := b.Finish()
	must(err)
	return msg
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// goResolverTo returns a Go resolver that sends every query to a fake
// server (the OS resolver of the SystemOnly tests).
func goResolverTo(s *fakeServer) *net.Resolver {
	addr := net.JoinHostPort(s.ip, strconv.Itoa(s.port))
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
}

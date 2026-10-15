package dnscheck

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// The DNS client of the verification: one question per exchange, over UDP
// with EDNS0, again over TCP when the UDP reply is truncated. Only a reply
// whose ID and question match the query is accepted; anything else that
// arrives on the socket is ignored until the deadline.

const (
	// maxUDPPayload is the EDNS0 payload size advertised and the read
	// buffer of a UDP reply. A TCP reply is bounded by its 2-byte length.
	maxUDPPayload = 4096
)

// queryType is a record type the verification asks for.
type queryType = dnsmessage.Type

const (
	typeA    = dnsmessage.TypeA
	typeAAAA = dnsmessage.TypeAAAA
	typeMX   = dnsmessage.TypeMX
	typeNS   = dnsmessage.TypeNS
	typeTXT  = dnsmessage.TypeTXT
)

// errUnexpectedReply is a TCP reply that does not answer the query.
var errUnexpectedReply = errors.New("unexpected reply")

// tcpError is the failure of the TCP exchange after a truncated UDP reply.
type tcpError struct{ err error }

func (e *tcpError) Error() string { return "truncated; over TCP: " + e.err.Error() }
func (e *tcpError) Unwrap() error { return e.err }

// reply is the part of a DNS reply the verification reads.
type reply struct {
	rcode         dnsmessage.RCode
	authoritative bool
	answers       []dnsmessage.Resource
}

// typeOf returns the record type of a normalized qtype (MX, A, AAAA).
func typeOf(qtype string) queryType {
	switch qtype {
	case "A":
		return typeA
	case "AAAA":
		return typeAAAA
	}
	return typeMX
}

// typeName is the name of a record type in the evidence.
func typeName(t queryType) string {
	switch t {
	case typeA:
		return "A"
	case typeAAAA:
		return "AAAA"
	case typeMX:
		return "MX"
	case typeNS:
		return "NS"
	case typeTXT:
		return "TXT"
	}
	return t.String()
}

// lookupTarget asks one server the question of a verification and returns
// the answer and its evidence ("MX ok (mx1.example.jp)"). For A and AAAA,
// no data counts as data: the name exists without records of that type (an
// IPv4-only host asked for AAAA), which is all a failed lookup of the name
// is verified for. For MX, a name without MX records is asked for its A and
// then AAAA records: mail goes to the address of the name itself then (the
// implicit MX, RFC 5321 5.1), so an address counts as data, and a name with
// neither stays no data. auth asks an authoritative server (no recursion;
// only an authoritative reply counts).
func (c *Checker) lookupTarget(ctx context.Context, server netip.AddrPort, name, qtype string, auth bool) (answer, string) {
	t := typeOf(qtype)
	a, _, note := c.lookupType(ctx, server, name, t, auth)
	if t != typeMX {
		if a == answerNoData {
			return answerData, qtype + " " + note + " (no " + qtype + " record, name exists)"
		}
		return a, qtype + " " + note
	}
	if a != answerNoData {
		return a, qtype + " " + note
	}
	notes := []string{"MX none"}
	result := answerNoData
	for _, t := range []queryType{typeA, typeAAAA} {
		got, _, note := c.lookupType(ctx, server, name, t, auth)
		notes = append(notes, typeName(t)+" "+note)
		result = combine(result, got)
		if got == answerData {
			break
		}
	}
	return result, strings.Join(notes, "; ")
}

// lookupType asks one server for the records of one type and classifies
// the reply: data (with the records as text), NXDOMAIN, no data, an error
// reply (fail), or none (no reply; from an authoritative server also a
// reply that is not authoritative, such as a referral to a delegated zone).
// From an authoritative server, a CNAME of the name counts as data: the
// name exists, and its target may lie in another zone.
func (c *Checker) lookupType(ctx context.Context, server netip.AddrPort, name string, t queryType, auth bool) (answer, []string, string) {
	r, err := c.exchange(ctx, server, name, t, !auth)
	if err != nil {
		return answerNone, nil, describeError(err)
	}
	if r.rcode != dnsmessage.RCodeSuccess && r.rcode != dnsmessage.RCodeNameError {
		return answerFail, nil, rcodeName(r.rcode)
	}
	if auth && !r.authoritative {
		return answerNone, nil, "not authoritative for the name"
	}
	if r.rcode == dnsmessage.RCodeNameError {
		return answerNX, nil, "NXDOMAIN"
	}
	if values := r.records(t); len(values) > 0 {
		return answerData, values, "ok (" + quote(values) + ")"
	}
	if auth {
		if target := r.cname(name); target != "" {
			return answerData, nil, "CNAME " + target
		}
	}
	return answerNoData, nil, "no data"
}

// exchange sends one question to a server (recursion desired for a
// resolver, not for an authoritative server) and returns the matching
// reply: over UDP, and over TCP when the UDP reply is truncated. Each
// transport is bounded by the per-query timeout and by ctx. Whether an
// authoritative server answered is recorded (noteAuthReply).
func (c *Checker) exchange(ctx context.Context, server netip.AddrPort, name string, t queryType, recursive bool) (*reply, error) {
	q, msg, id, err := newQuery(name, t, recursive)
	if err != nil {
		return nil, err
	}
	r, truncated, err := c.exchangeUDP(ctx, server, msg, id, q)
	if !recursive && !ended(ctx) {
		c.noteAuthReply(server, err == nil)
	}
	if err == nil && truncated {
		r, err = c.exchangeTCP(ctx, server, msg, id, q)
		if err != nil {
			err = &tcpError{err: err}
		}
	}
	return r, err
}

// exchangeUDP sends the query over a connected UDP socket (the kernel drops
// datagrams from other addresses) and reads until a reply matches it.
func (c *Checker) exchangeUDP(ctx context.Context, server netip.AddrPort, msg []byte, id uint16, q dnsmessage.Question) (*reply, bool, error) {
	conn, done, err := c.dial(ctx, "udp", server)
	if err != nil {
		return nil, false, err
	}
	defer done()
	if _, err := conn.Write(msg); err != nil {
		return nil, false, err
	}
	buf := make([]byte, maxUDPPayload)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, false, err
		}
		if r, truncated, ok := parseReply(buf[:n], id, q); ok {
			return r, truncated, nil
		}
		// Not the reply to this query (another ID or question, or
		// unreadable): keep waiting for it until the deadline.
	}
}

// exchangeTCP sends the query over TCP (2-byte length prefix) and reads one
// reply, which must match the query.
func (c *Checker) exchangeTCP(ctx context.Context, server netip.AddrPort, msg []byte, id uint16, q dnsmessage.Question) (*reply, error) {
	conn, done, err := c.dial(ctx, "tcp", server)
	if err != nil {
		return nil, err
	}
	defer done()
	frame := make([]byte, 2+len(msg))
	binary.BigEndian.PutUint16(frame, uint16(len(msg)))
	copy(frame[2:], msg)
	if _, err := conn.Write(frame); err != nil {
		return nil, err
	}
	var size [2]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return nil, err
	}
	buf := make([]byte, binary.BigEndian.Uint16(size[:]))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, err
	}
	r, truncated, ok := parseReply(buf, id, q)
	if !ok || truncated {
		return nil, errUnexpectedReply
	}
	return r, nil
}

// dial connects to a server for one exchange. The connection's deadline is
// the per-query timeout, moved to the past when ctx is canceled; done
// releases everything.
func (c *Checker) dial(ctx context.Context, network string, server netip.AddrPort) (net.Conn, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, server.String())
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline) // the deadline of a fresh connection cannot fail to be set
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	return conn, func() {
		stop()
		cancel()
		conn.Close()
	}, nil
}

// newQuery builds the query of one question with a random ID, the
// recursion-desired flag and an EDNS0 record advertising maxUDPPayload.
func newQuery(name string, t queryType, recursive bool) (dnsmessage.Question, []byte, uint16, error) {
	n, err := dnsmessage.NewName(name + ".")
	if err != nil {
		return dnsmessage.Question{}, nil, 0, err
	}
	q := dnsmessage.Question{Name: n, Type: t, Class: dnsmessage.ClassINET}
	id := randomID()
	b := dnsmessage.NewBuilder(make([]byte, 0, 512), dnsmessage.Header{ID: id, RecursionDesired: recursive})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return q, nil, 0, err
	}
	if err := b.Question(q); err != nil {
		return q, nil, 0, err
	}
	if err := b.StartAdditionals(); err != nil {
		return q, nil, 0, err
	}
	var opt dnsmessage.ResourceHeader
	if err := opt.SetEDNS0(maxUDPPayload, dnsmessage.RCodeSuccess, false); err != nil {
		return q, nil, 0, err
	}
	if err := b.OPTResource(opt, dnsmessage.OPTResource{}); err != nil {
		return q, nil, 0, err
	}
	msg, err := b.Finish()
	return q, msg, id, err
}

// randomID returns an unpredictable message ID.
func randomID() uint16 {
	var b [2]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails (it crashes the program instead)
	return binary.BigEndian.Uint16(b[:])
}

// parseReply reads a reply and reports whether it answers the query (a
// response with the same ID and the same single question). A truncated
// reply is reported without its records.
func parseReply(b []byte, id uint16, q dnsmessage.Question) (*reply, bool, bool) {
	var p dnsmessage.Parser
	h, err := p.Start(b)
	if err != nil || !h.Response || h.ID != id {
		return nil, false, false
	}
	questions, err := p.AllQuestions()
	if err != nil || len(questions) != 1 || !sameQuestion(questions[0], q) {
		return nil, false, false
	}
	if h.Truncated {
		return nil, true, true
	}
	answers, err := p.AllAnswers()
	if err != nil {
		return nil, false, false
	}
	return &reply{rcode: h.RCode, authoritative: h.Authoritative, answers: answers}, false, true
}

// sameQuestion compares two questions; the name case-insensitively.
func sameQuestion(a, b dnsmessage.Question) bool {
	return a.Type == b.Type && a.Class == b.Class && strings.EqualFold(a.Name.String(), b.Name.String())
}

// records returns the records of type t in the answer section as text:
// host names (MX in order of preference, NS) lower-cased without the
// trailing dot, addresses (A, AAAA), and the text of a TXT record (its
// character-strings joined without anything in between, RFC 7208 3.3).
// Records of a CNAME chain the resolver followed are included; the chain is
// not followed here.
func (r *reply) records(t queryType) []string {
	type mx struct {
		pref uint16
		host string
	}
	var mxs []mx
	var out []string
	for _, rr := range r.answers {
		if rr.Header.Type != t || rr.Header.Class != dnsmessage.ClassINET {
			continue
		}
		switch body := rr.Body.(type) {
		case *dnsmessage.MXResource:
			mxs = append(mxs, mx{pref: body.Pref, host: hostText(body.MX.String())})
		case *dnsmessage.NSResource:
			out = append(out, hostText(body.NS.String()))
		case *dnsmessage.AResource:
			out = append(out, netip.AddrFrom4(body.A).String())
		case *dnsmessage.AAAAResource:
			out = append(out, netip.AddrFrom16(body.AAAA).String())
		case *dnsmessage.TXTResource:
			out = append(out, strings.Join(body.TXT, ""))
		}
	}
	sort.SliceStable(mxs, func(i, j int) bool { return mxs[i].pref < mxs[j].pref })
	for _, m := range mxs {
		out = append(out, m.host)
	}
	return out
}

// cname returns the target of a CNAME record of the name in the answer
// section ("" when there is none).
func (r *reply) cname(name string) string {
	for _, rr := range r.answers {
		if body, ok := rr.Body.(*dnsmessage.CNAMEResource); ok && strings.EqualFold(rr.Header.Name.String(), name+".") {
			return hostText(body.CNAME.String())
		}
	}
	return ""
}

// hostText returns a host name from a record for the evidence and for
// further lookups: normalized, "." for the root (a null MX), or
// "(invalid name)" for anything that is no host name, so that nothing a
// record holds reaches the evidence unchecked.
func hostText(s string) string {
	if s == "." {
		return "."
	}
	if n, ok := normalizeName(s); ok {
		return n
	}
	return "(invalid name)"
}

// rcodeName is the name of a response code in the evidence.
func rcodeName(rc dnsmessage.RCode) string {
	switch rc {
	case dnsmessage.RCodeFormatError:
		return "FORMERR"
	case dnsmessage.RCodeServerFailure:
		return "SERVFAIL"
	case dnsmessage.RCodeNameError:
		return "NXDOMAIN"
	case dnsmessage.RCodeNotImplemented:
		return "NOTIMP"
	case dnsmessage.RCodeRefused:
		return "REFUSED"
	}
	return fmt.Sprintf("rcode %d", int(rc))
}

// describeError words a failed exchange for the evidence: "timeout", or "no
// response" with the reason the operating system gave (without addresses),
// prefixed when it happened over TCP after a truncated UDP reply.
func describeError(err error) string {
	var te *tcpError
	if errors.As(err, &te) {
		return "truncated; over TCP " + describeError(te.err)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, errUnexpectedReply) {
		return "unexpected reply"
	}
	inner := err
	for next := errors.Unwrap(inner); next != nil; next = errors.Unwrap(inner) {
		inner = next
	}
	return fmt.Sprintf("no response (%s)", inner.Error())
}

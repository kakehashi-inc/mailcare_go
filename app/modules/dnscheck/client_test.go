package dnscheck

import (
	"bytes"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"mailcare/app/modules/mailengine"
)

// queryOptions parses a query and returns its recursion-desired flag and the
// options of its EDNS0 record; the query must hold exactly one OPT record.
func queryOptions(t *testing.T, msg []byte) (bool, []dnsmessage.Option) {
	t.Helper()
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SkipAllQuestions(); err != nil {
		t.Fatal(err)
	}
	if err := p.SkipAllAnswers(); err != nil {
		t.Fatal(err)
	}
	if err := p.SkipAllAuthorities(); err != nil {
		t.Fatal(err)
	}
	var options []dnsmessage.Option
	opts := 0
	for {
		rh, err := p.AdditionalHeader()
		if err == dnsmessage.ErrSectionDone {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if rh.Type != dnsmessage.TypeOPT {
			if err := p.SkipAdditional(); err != nil {
				t.Fatal(err)
			}
			continue
		}
		opt, err := p.OPTResource()
		if err != nil {
			t.Fatal(err)
		}
		opts++
		options = opt.Options
	}
	if opts != 1 {
		t.Fatalf("%d OPT records, want 1", opts)
	}
	return h.RecursionDesired, options
}

// isECSOptOut reports whether the options are exactly the ECS option that
// asks for no subnet: code 8, family 1, source and scope prefix lengths 0,
// no address bytes (RFC 7871 7.1.2).
func isECSOptOut(options []dnsmessage.Option) bool {
	return len(options) == 1 && options[0].Code == 8 && bytes.Equal(options[0].Data, []byte{0, 1, 0, 0})
}

// TestQueryECSOptOut checks the EDNS0 record of the queries: a recursive
// query (a resolver, the control query) carries exactly the ECS option that
// asks the resolver to forward no part of this server's address, a query to
// an authoritative server carries no option, and the queries a Checker sends
// are built that way while the fake servers still answer them.
func TestQueryECSOptOut(t *testing.T) {
	for _, recursive := range []bool{true, false} {
		_, msg, _, err := newQuery("example.jp", typeMX, recursive)
		if err != nil {
			t.Fatal(err)
		}
		rd, options := queryOptions(t, msg)
		if rd != recursive {
			t.Errorf("recursive %v: RD flag %v", recursive, rd)
		}
		if recursive && !isECSOptOut(options) {
			t.Errorf("recursive query: options %+v, want only the ECS opt-out", options)
		}
		if !recursive && len(options) != 0 {
			t.Errorf("authoritative query: options %+v, want none", options)
		}
	}

	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	mailHost(s, "mx1.example.jp", "192.0.2.1")
	withAuthority(s)
	c := newTestChecker(nil, s.port, "127.0.0.1")
	if got, ok := verify(t, c, "example.jp", "MX", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	var recursive, auth int
	for _, msg := range s.rawReceived() {
		rd, options := queryOptions(t, msg)
		switch {
		case rd && !isECSOptOut(options):
			t.Errorf("recursive query on the wire: options %+v", options)
		case !rd && len(options) != 0:
			t.Errorf("authoritative query on the wire: options %+v", options)
		}
		if rd {
			recursive++
		} else {
			auth++
		}
	}
	if recursive == 0 || auth == 0 {
		t.Errorf("%d recursive and %d authoritative queries; want both kinds", recursive, auth)
	}
}

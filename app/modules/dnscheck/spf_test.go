package dnscheck

import (
	"context"
	"fmt"
	"math/big"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// prefixes parses the expected prefixes of an SPF result.
func prefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, p := range s {
		out[i] = netip.MustParsePrefix(p)
	}
	return out
}

// txtQueries returns the TXT questions a fake server received.
func txtQueries(s *fakeServer) []string {
	var out []string
	for _, q := range s.received() {
		if strings.Fields(q)[2] == "TXT" {
			out = append(out, q)
		}
	}
	return out
}

// TestLookupSPFTerms evaluates a record with every supported term: ip4 and
// ip6, a and mx with and without a domain and with dual CIDR lengths
// applied to the addresses found, include and redirect (followed only
// without an all mechanism, whatever its qualifier); only the terms with
// the pass qualifier (none or "+") count, the others ("-", "~", "?") are
// skipped, an include with its whole tree; exists, ptr, exp, unknown
// modifiers and macros are ignored. The record is split into
// character-strings, joined without separator.
func TestLookupSPFTerms(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp TXT", data("google-site-verification=abc",
		"v=spf1 ip4:192.0.2.1 -ip4:198.51.100.0/24 ~ip6:2001:db8:5::/48 ?a:skipped.example.jp +ip6:2001:db8::/32 +a "+
			"a:web.example.jp/24 mx/28//64 |mx:other.example.jp include:_spf.example.net -include:fail.example.net "+
			"~include:soft.example.net ?include:neutral.example.net -mx:skipped.example.jp ~a |"+
			"exists:%{i}.x.example.jp ptr a:%{d}.example.jp exp=explain.example.jp foo=bar redirect=ignored.example.jp ~all"))
	for _, skipped := range []string{"fail.example.net", "soft.example.net", "neutral.example.net"} {
		s.resolve(skipped+" TXT", data("v=spf1 ip4:10.1.0.0/16"))
	}
	s.resolve("skipped.example.jp A", data("10.2.0.1"))
	s.resolve("skipped.example.jp MX", data("mx.skipped.example.jp"))
	s.resolve("example.jp A", data("203.0.113.1"))
	s.resolve("example.jp AAAA", data("2001:db8:1::1"))
	s.resolve("example.jp MX", data("mx1.example.jp"))
	s.resolve("web.example.jp A", data("203.0.113.77"))
	s.resolve("mx1.example.jp A", data("192.0.2.130"))
	s.resolve("mx1.example.jp AAAA", data("2001:db8:2::25"))
	s.resolve("other.example.jp MX", data("mx2.example.jp", "mx3.example.jp"))
	s.resolve("mx2.example.jp A", data("198.51.100.200"))
	s.resolve("mx3.example.jp A", answerWith(fakeNX))
	s.resolve("_spf.example.net TXT", data("v=spf1 ip4:203.0.113.128/25 redirect=final.example.net"))
	s.resolve("final.example.net TXT", data("V=SPF1 ip6:2001:db8:9::/48 -all"))
	c := newTestChecker(nil, s.port, "127.0.0.1")
	got := c.LookupSPF(context.Background(), "Example.JP.")
	want := SPFResult{
		Prefixes: prefixes("192.0.2.1/32", "2001:db8::/32", "203.0.113.1/32", "2001:db8:1::1/128",
			"203.0.113.0/24", "192.0.2.128/28", "2001:db8:2::/64", "198.51.100.200/32", "203.0.113.128/25", "2001:db8:9::/48"),
		Hosts:    []string{"example.jp", "web.example.jp", "mx1.example.jp", "mx2.example.jp", "mx3.example.jp"},
		Complete: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LookupSPF =\n%+v\nwant\n%+v", got, want)
	}
	for _, q := range s.received() {
		for _, ignored := range []string{"ignored.example.jp", "x.example.jp", "explain.example.jp", "skipped.example.jp",
			"fail.example.net", "soft.example.net", "neutral.example.net"} {
			if strings.Contains(q, ignored) {
				t.Errorf("an ignored term was looked up: %q", q)
			}
		}
	}
	// Once per run.
	before := len(s.received())
	if again := c.LookupSPF(context.Background(), "example.jp"); !reflect.DeepEqual(again, want) || len(s.received()) != before {
		t.Errorf("second call: %+v, %d new questions", again, len(s.received())-before)
	}
	// The mailengine form.
	if p, h, ok := c.ResolveSPF(context.Background(), "example.jp"); !ok || !reflect.DeepEqual(p, want.Prefixes) || !reflect.DeepEqual(h, want.Hosts) {
		t.Errorf("ResolveSPF = %v %v %v", p, h, ok)
	}
}

// TestLookupSPFNoRecord: NXDOMAIN, a name without TXT records and TXT
// records without "v=spf1" are complete without addresses, and so is a
// loop, which is skipped.
func TestLookupSPFNoRecord(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("nodata.example.jp A", data("192.0.2.1"))
	s.resolve("other.example.jp TXT", data("v=spf10 ip4:192.0.2.1", "v=DMARC1; p=none", "spf1 ip4:192.0.2.2"))
	s.resolve("loop.example.jp TXT", data("v=spf1 ip4:192.0.2.9 include:back.example.jp"))
	s.resolve("back.example.jp TXT", data("v=spf1 include:loop.example.jp redirect=back.example.jp"))
	c := newTestChecker(nil, s.port, "127.0.0.1")
	for _, tc := range []struct {
		domain string
		want   SPFResult
	}{
		{"nxdomain.example.jp", SPFResult{Complete: true}},
		{"nodata.example.jp", SPFResult{Complete: true}},
		{"other.example.jp", SPFResult{Complete: true}},
		{"loop.example.jp", SPFResult{Prefixes: prefixes("192.0.2.9/32"), Complete: true}},
	} {
		if got := c.LookupSPF(context.Background(), tc.domain); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %+v, want %+v", tc.domain, got, tc.want)
		}
	}
}

// TestLookupSPFIncomplete: a lookup that fails (SERVFAIL, no answer), two
// SPF records, a malformed term, and every bound (depth, queries, prefixes,
// MX targets) leave the evaluation incomplete, keeping what was found
// before.
func TestLookupSPFIncomplete(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("servfail.example.jp TXT", answerWith(fakeServFail))
	s.resolve("drop.example.jp TXT", answerWith(fakeDrop))
	s.resolve("include-fail.example.jp TXT", data("v=spf1 ip4:192.0.2.1 include:servfail.example.jp"))
	s.resolve("a-fail.example.jp TXT", data("v=spf1 a:host.example.jp"))
	s.resolve("host.example.jp A", answerWith(fakeServFail))
	s.resolve("two.example.jp TXT", data("v=spf1 ip4:192.0.2.1", "v=spf1 ip4:192.0.2.2"))
	for name, record := range map[string]string{
		"unknown.example.jp":   "v=spf1 ip4:192.0.2.1 foo:bar",
		"unknownq.example.jp":  "v=spf1 ip4:192.0.2.1 -foo:bar",
		"badip4.example.jp":    "v=spf1 ip4:2001:db8::1",
		"badip6.example.jp":    "v=spf1 ip6:192.0.2.1",
		"badcidr.example.jp":   "v=spf1 a/33",
		"badinc.example.jp":    "v=spf1 include",
		"redirects.example.jp": "v=spf1 redirect=a.example.jp redirect=b.example.jp",
		"baddomain.example.jp": "v=spf1 include:bad..example.jp",
	} {
		s.resolve(name+" TXT", data(record))
	}
	// A chain of includes eleven deep.
	for i := 0; i <= 11; i++ {
		s.resolve(fmt.Sprintf("d%d.example.jp TXT", i), data(fmt.Sprintf("v=spf1 ip4:10.0.0.%d include:d%d.example.jp", i+1, i+1)))
	}
	// Many a terms: more queries than allowed.
	var terms []string
	for i := 0; i < 16; i++ {
		terms = append(terms, fmt.Sprintf("a:h%d.example.jp", i))
		s.resolve(fmt.Sprintf("h%d.example.jp A", i), data(fmt.Sprintf("192.0.2.%d", i+1)))
	}
	s.resolve("queries.example.jp TXT", data("v=spf1 "+strings.Join(terms[:8], " ")+"|"+" "+strings.Join(terms[8:], " ")))
	// More MX targets than allowed.
	var mxs []string
	for i := 0; i < 11; i++ {
		mxs = append(mxs, fmt.Sprintf("mx%d.example.jp", i))
	}
	s.resolve("manymx.example.jp TXT", data("v=spf1 mx"))
	s.resolve("manymx.example.jp MX", data(mxs...))
	s.resolve("prefixes.example.jp TXT", data("v=spf1 ip4:192.0.2.1 ip4:192.0.2.2 ip4:192.0.2.3"))
	c := newTestChecker(nil, s.port, "127.0.0.1")
	c.spfMaxPrefixes = 2
	for _, tc := range []struct {
		domain string
		found  []netip.Prefix
	}{
		{"servfail.example.jp", nil},
		{"drop.example.jp", nil},
		{"include-fail.example.jp", prefixes("192.0.2.1/32")},
		{"a-fail.example.jp", nil},
		{"two.example.jp", nil},
		{"unknown.example.jp", prefixes("192.0.2.1/32")},
		{"unknownq.example.jp", prefixes("192.0.2.1/32")},
		{"badip4.example.jp", nil},
		{"badip6.example.jp", nil},
		{"badcidr.example.jp", nil},
		{"badinc.example.jp", nil},
		{"redirects.example.jp", nil},
		{"baddomain.example.jp", nil},
		{"d0.example.jp", nil},
		{"queries.example.jp", nil},
		{"manymx.example.jp", nil},
		{"prefixes.example.jp", prefixes("192.0.2.1/32", "192.0.2.2/32")},
	} {
		got := c.LookupSPF(context.Background(), tc.domain)
		if got.Complete {
			t.Errorf("%s: complete, want incomplete: %+v", tc.domain, got)
		}
		if tc.found != nil && !reflect.DeepEqual(got.Prefixes, tc.found) {
			t.Errorf("%s: prefixes %v, want %v", tc.domain, got.Prefixes, tc.found)
		}
	}
	// The depth bound: ten levels below the domain are still followed.
	c2 := newTestChecker(nil, s.port, "127.0.0.1")
	s.resolve("d10.example.jp TXT", data("v=spf1 ip4:10.0.0.11"))
	if got := c2.LookupSPF(context.Background(), "d0.example.jp"); !got.Complete || len(got.Prefixes) != 11 {
		t.Errorf("ten levels deep: %+v", got)
	}
	// The query bound: 30 queries are allowed.
	c3 := newTestChecker(nil, s.port, "127.0.0.1")
	s.resolve("fifteen.example.jp TXT", data("v=spf1 "+strings.Join(terms[:14], " ")+"|"+" "+terms[14]))
	if got := c3.LookupSPF(context.Background(), "fifteen.example.jp"); got.Complete {
		t.Errorf("31 queries: %+v, want incomplete", got)
	}
	c4 := newTestChecker(nil, s.port, "127.0.0.1")
	s.resolve("fourteen.example.jp TXT", data("v=spf1 "+strings.Join(terms[:14], " ")))
	if got := c4.LookupSPF(context.Background(), "fourteen.example.jp"); !got.Complete || len(got.Prefixes) != 14 {
		t.Errorf("29 queries: %+v, want complete", got)
	}
}

// TestLookupSPFWideRanges: a term with the pass qualifier that yields a
// prefix shorter than an IPv4 /8 or an IPv6 /16 makes the evaluation
// incomplete, whether it is written in an ip4 or ip6 term, reached through
// an include (a third party listing the whole Internet), or made by the
// CIDR length of an a or mx term from the addresses found; what was found
// before is kept. A /8 and a /16 are accepted (with an address inside the
// /8: one outside would go past the total address space, see
// TestLookupSPFAddressSpace); an a term with a short CIDR
// length that finds no address yields nothing; a wide range with another
// qualifier is skipped like every such term.
func TestLookupSPFWideRanges(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("evil.example.net TXT", data("v=spf1 ip4:0.0.0.0/0"))
	s.resolve("host.example.jp A", data("192.0.2.1"))
	s.resolve("mx6.example.jp AAAA", data("2001:db8::25"))
	records := map[string]string{
		"evil-include.example.jp": "v=spf1 ip4:192.0.2.1 include:evil.example.net",
		"all4.example.jp":         "v=spf1 ip4:192.0.2.1 ip4:0.0.0.0/0",
		"all6.example.jp":         "v=spf1 ip4:192.0.2.1 ip6:::/0",
		"ip4-7.example.jp":        "v=spf1 ip4:192.0.2.1 ip4:12.0.0.0/7",
		"ip4-8.example.jp":        "v=spf1 ip4:12.0.0.1 ip4:12.0.0.0/8",
		"ip6-15.example.jp":       "v=spf1 ip4:192.0.2.1 ip6:2001::/15",
		"ip6-16.example.jp":       "v=spf1 ip4:192.0.2.1 ip6:2001::/16",
		"a0.example.jp":           "v=spf1 ip4:192.0.2.1 a:host.example.jp/0",
		"a7.example.jp":           "v=spf1 ip4:192.0.2.1 a:host.example.jp/7",
		"a8.example.jp":           "v=spf1 ip4:192.0.2.1 a:host.example.jp/8",
		"a0-none.example.jp":      "v=spf1 ip4:192.0.2.1 a:none.example.jp/0//0",
		"mx0.example.jp":          "v=spf1 ip4:192.0.2.1 mx//0",
		"mx15.example.jp":         "v=spf1 ip4:192.0.2.1 mx//15",
		"mx16.example.jp":         "v=spf1 ip4:192.0.2.1 mx//16",
		"fail-wide.example.jp":    "v=spf1 ip4:192.0.2.1 -ip4:0.0.0.0/0 ~ip6:::/0 ?include:evil.example.net -all",
	}
	for name, record := range records {
		s.resolve(name+" TXT", data(record))
		if strings.HasPrefix(name, "mx") {
			s.resolve(name+" MX", data("mx6.example.jp"))
		}
	}
	c := newTestChecker(nil, s.port, "127.0.0.1")
	for _, tc := range []struct {
		domain   string
		complete bool
		want     []netip.Prefix
	}{
		{"evil-include.example.jp", false, prefixes("192.0.2.1/32")},
		{"all4.example.jp", false, prefixes("192.0.2.1/32")},
		{"all6.example.jp", false, prefixes("192.0.2.1/32")},
		{"ip4-7.example.jp", false, prefixes("192.0.2.1/32")},
		{"ip4-8.example.jp", true, prefixes("12.0.0.1/32", "12.0.0.0/8")},
		{"ip6-15.example.jp", false, prefixes("192.0.2.1/32")},
		{"ip6-16.example.jp", true, prefixes("192.0.2.1/32", "2001::/16")},
		{"a0.example.jp", false, prefixes("192.0.2.1/32")},
		{"a7.example.jp", false, prefixes("192.0.2.1/32")},
		{"a8.example.jp", true, prefixes("192.0.2.1/32", "192.0.0.0/8")},
		{"a0-none.example.jp", true, prefixes("192.0.2.1/32")},
		{"mx0.example.jp", false, prefixes("192.0.2.1/32")},
		{"mx15.example.jp", false, prefixes("192.0.2.1/32")},
		{"mx16.example.jp", true, prefixes("192.0.2.1/32", "2001::/16")},
		{"fail-wide.example.jp", true, prefixes("192.0.2.1/32")},
	} {
		got := c.LookupSPF(context.Background(), tc.domain)
		if got.Complete != tc.complete || !reflect.DeepEqual(got.Prefixes, tc.want) {
			t.Errorf("%s: %+v, want complete %v with %v", tc.domain, got, tc.complete, tc.want)
		}
	}
}

// TestLookupSPFMappedIPv6: an ip6 term of an IPv4-mapped address counts as
// the IPv4 prefix of its length - 96 (the received addresses are compared
// unmapped); one shorter than 96 is ignored, and one that maps to a range
// wider than an IPv4 /8 makes the evaluation incomplete.
func TestLookupSPFMappedIPv6(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("mapped.example.jp TXT", data("v=spf1 ip6:::ffff:192.0.2.1 ip6:::FFFF:198.51.100.0/120 ip6:::ffff:203.0.113.0/95 ip6:2001:db8::/32"))
	s.resolve("mapped-wide.example.jp TXT", data("v=spf1 ip6:::ffff:192.0.2.1 ip6:::ffff:0.0.0.0/100"))
	s.resolve("mapped-ip4.example.jp TXT", data("v=spf1 ip4:::ffff:192.0.2.1"))
	c := newTestChecker(nil, s.port, "127.0.0.1")
	if got := c.LookupSPF(context.Background(), "mapped.example.jp"); !got.Complete ||
		!reflect.DeepEqual(got.Prefixes, prefixes("192.0.2.1/32", "198.51.100.0/24", "2001:db8::/32")) {
		t.Errorf("mapped: %+v", got)
	}
	if got := c.LookupSPF(context.Background(), "mapped-wide.example.jp"); got.Complete ||
		!reflect.DeepEqual(got.Prefixes, prefixes("192.0.2.1/32")) {
		t.Errorf("mapped, wide: %+v", got)
	}
	if got := c.LookupSPF(context.Background(), "mapped-ip4.example.jp"); got.Complete {
		t.Errorf("an IPv4-mapped address in an ip4 term: %+v, want malformed", got)
	}
}

// TestAddressSpace counts the addresses prefixes cover, each once:
// duplicates and prefixes inside others are not counted again, adjacent
// ones are merged, and the families are counted apart.
func TestAddressSpace(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     []netip.Prefix
		v4, v6 string
	}{
		{"none", nil, "0", "0"},
		{"one /8", prefixes("10.0.0.0/8"), "16777216", "0"},
		{"duplicates and nested", prefixes("10.0.0.0/8", "10.0.0.0/8", "10.1.0.0/16", "10.0.0.0/9", "10.255.255.255/32"), "16777216", "0"},
		{"adjacent halves", prefixes("10.128.0.0/9", "10.0.0.0/9"), "16777216", "0"},
		{"apart", prefixes("10.0.0.0/8", "192.0.2.1/32", "192.0.2.0/31"), "16777218", "0"},
		{"families apart", prefixes("192.0.2.0/24", "2001:db8::/126", "2001:db8::1/128"), "256", "4"},
		{"two ipv6 /16", prefixes("2001::/16", "2002::/16", "2001:db8::/32"), "0", new(big.Int).Lsh(big.NewInt(1), 113).String()},
	} {
		if v4, v6 := addressSpace(tc.in, true).String(), addressSpace(tc.in, false).String(); v4 != tc.v4 || v6 != tc.v6 {
			t.Errorf("%s: %s / %s, want %s / %s", tc.name, v4, v6, tc.v4, tc.v6)
		}
	}
}

// TestLookupSPFAddressSpace: an evaluation whose ranges together cover more
// than one IPv4 /8 or one IPv6 /16 is incomplete, even when no single range
// is wider than that (256 terms of a /8 each, spread over includes, cover
// every IPv4 address); exactly that much is complete, and a range listed
// again or inside another, in the record or in an include, is counted once.
func TestLookupSPFAddressSpace(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	var includes []string
	for part := 0; part < 4; part++ {
		var terms []string
		for n := part * 64; n < (part+1)*64; n++ {
			terms = append(terms, fmt.Sprintf("ip4:%d.0.0.0/8", n))
		}
		name := fmt.Sprintf("part%d.example.net", part)
		// Character-strings of at most 255 bytes, as in real records.
		var strs []string
		for i := 0; i < len(terms); i += 10 {
			strs = append(strs, strings.Join(terms[i:min(i+10, len(terms))], " ")+" ")
		}
		s.resolve(name+" TXT", data("v=spf1 "+strings.Join(strs, "|")+"~all"))
		includes = append(includes, "include:"+name)
	}
	records := map[string]string{
		"all-by-eights.example.jp": "v=spf1 ip4:192.0.2.1 " + strings.Join(includes, " ") + " -all",
		"seventeen.example.jp": "v=spf1 ip4:10.0.0.0/12 ip4:10.16.0.0/12 ip4:10.32.0.0/12 ip4:10.48.0.0/12 ip4:10.64.0.0/12 " +
			"ip4:10.80.0.0/12 ip4:10.96.0.0/12 ip4:10.112.0.0/12 ip4:10.128.0.0/12 ip4:10.144.0.0/12 ip4:10.160.0.0/12 " +
			"|ip4:10.176.0.0/12 ip4:10.192.0.0/12 ip4:10.208.0.0/12 ip4:10.224.0.0/12 ip4:10.240.0.0/12 ip4:11.0.0.0/12 -all",
		"one-eight.example.jp":     "v=spf1 ip4:10.0.0.0/9 ip4:10.128.0.0/9 include:again.example.net ip4:10.1.2.3 -all",
		"two-sixteens.example.jp":  "v=spf1 ip6:2001::/16 ip6:2002::/16 -all",
		"one-sixteen.example.jp":   "v=spf1 ip6:2001::/16 ip6:2001:db8::/32 include:again.example.net -all",
		"again.example.net":        "v=spf1 ip4:10.0.0.0/8 ip6:2001::/17 ~all",
		"sixteen-and-more.example": "v=spf1 ip6:2001::/16 ip6:2002::1 -all",
	}
	for name, record := range records {
		s.resolve(name+" TXT", data(record))
	}
	c := newTestChecker(nil, s.port, "127.0.0.1")
	for domain, complete := range map[string]bool{
		"all-by-eights.example.jp": false,
		"seventeen.example.jp":     false,
		"one-eight.example.jp":     true,
		"two-sixteens.example.jp":  false,
		"one-sixteen.example.jp":   true,
		"sixteen-and-more.example": false,
	} {
		got := c.LookupSPF(context.Background(), domain)
		if got.Complete != complete || len(got.Prefixes) == 0 {
			t.Errorf("%s: complete %v with %d prefixes, want complete %v", domain, got.Complete, len(got.Prefixes), complete)
		}
	}
	if got := c.LookupSPF(context.Background(), "all-by-eights.example.jp"); len(got.Prefixes) != 257 {
		t.Errorf("all by eights: %d prefixes kept, want 257", len(got.Prefixes))
	}
}

// TestSPFTooWide checks the limits a stored SPF result is held to as well:
// a single range wider than an IPv4 /8 or an IPv6 /16, or more address
// space together than one of each, every address counted once.
func TestSPFTooWide(t *testing.T) {
	var twelves []netip.Prefix
	for i := range 17 {
		twelves = append(twelves, netip.MustParsePrefix(fmt.Sprintf("10.%d.0.0/12", i*16%256)).Masked())
	}
	twelves[16] = netip.MustParsePrefix("11.0.0.0/12")
	for _, tc := range []struct {
		name string
		in   []netip.Prefix
		want bool
	}{
		{"none", nil, false},
		{"a /8", prefixes("12.0.0.0/8", "12.0.0.1/32"), false},
		{"a /7", prefixes("12.0.0.0/7"), true},
		{"an IPv6 /16", prefixes("2001::/16"), false},
		{"an IPv6 /15", prefixes("2000::/15"), true},
		{"seventeen /12", twelves, true},
		{"sixteen /12", twelves[:16], false},
		{"two IPv6 /16", prefixes("2001::/16", "2002::/16"), true},
	} {
		if got := SPFTooWide(tc.in); got != tc.want {
			t.Errorf("%s: SPFTooWide = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// loadSPFFixture serves the TXT records of testdata/spf_real_senders.txt
// from a fake server (see the header of the file).
func loadSPFFixture(t *testing.T, s *fakeServer) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "spf_real_senders.txt"))
	if err != nil {
		t.Fatal(err)
	}
	records := map[string][]string{}
	var names []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimRight(line, "\r"); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 || parts[1] != "TXT" {
			t.Fatalf("fixture line %q", line)
		}
		if _, ok := records[parts[0]]; !ok {
			names = append(names, parts[0])
		}
		records[parts[0]] = append(records[parts[0]], parts[2])
	}
	for _, name := range names {
		s.resolve(name+" TXT", data(records[name]...))
	}
}

// TestLookupSPFRealSenders evaluates the recorded SPF records of large mail
// services (testdata/spf_real_senders.txt): each is complete, and so is a
// domain that includes the four of them, all well within the total
// address space of one IPv4 /8 and one IPv6 /16.
func TestLookupSPFRealSenders(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	loadSPFFixture(t, s)
	s.resolve("combined.example.jp TXT", data("v=spf1 include:_spf.google.com include:spf.protection.outlook.com "+
		"include:amazonses.com include:sendgrid.net -all"))
	c := newTestChecker(nil, s.port, "127.0.0.1")
	for _, tc := range []struct {
		domain   string
		prefixes int
		v4       int64
	}{
		{"_spf.google.com", 8, 98304},
		{"_netblocks.google.com", 2, 98304},
		{"_netblocks2.google.com", 6, 0},
		{"_netblocks3.google.com", 0, 0},
		{"spf.protection.outlook.com", 11, 458752},
		{"amazonses.com", 13, 157696},
		{"sendgrid.net", 15, 241152},
		{"combined.example.jp", 47, 955904},
	} {
		got := c.LookupSPF(context.Background(), tc.domain)
		v4, v6 := addressSpace(got.Prefixes, true), addressSpace(got.Prefixes, false)
		t.Logf("%s: complete %v, %d prefixes, %s IPv4 and %s IPv6 addresses", tc.domain, got.Complete, len(got.Prefixes), v4, v6)
		if !got.Complete || len(got.Prefixes) != tc.prefixes || v4.Cmp(big.NewInt(tc.v4)) != 0 {
			t.Errorf("%s: complete %v, %d prefixes, %s IPv4 addresses; want complete, %d prefixes, %d IPv4 addresses",
				tc.domain, got.Complete, len(got.Prefixes), v4, tc.prefixes, tc.v4)
		}
		if v4.BitLen() > spfMaxSpaceBits4 || v6.BitLen() > spfMaxSpaceBits6 {
			t.Errorf("%s: %s IPv4 and %s IPv6 addresses reach the cap", tc.domain, v4, v6)
		}
	}
}

// TestLookupSPFOff: a Checker whose verification is off reads no SPF
// record (no TXT question at all) and its result is incomplete; a canceled
// evaluation is incomplete and not remembered.
func TestLookupSPFOff(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp TXT", data("v=spf1 ip4:192.0.2.1"))
	off := New(nil, Config{Enabled: false, Resolvers: []string{"127.0.0.1"}, Port: s.port, Timeout: testTimeout})
	if got := off.LookupSPF(context.Background(), "example.jp"); got.Complete || got.Prefixes != nil {
		t.Errorf("off: %+v", got)
	}
	if q := txtQueries(s); len(q) != 0 {
		t.Errorf("TXT questions with the verification off: %v", q)
	}
	c := newTestChecker(nil, s.port, "127.0.0.1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := c.LookupSPF(ctx, "example.jp"); got.Complete {
		t.Errorf("canceled: %+v", got)
	}
	if got := c.LookupSPF(context.Background(), "example.jp"); !got.Complete || !reflect.DeepEqual(got.Prefixes, prefixes("192.0.2.1/32")) {
		t.Errorf("after the cancellation: %+v", got)
	}
	if got := c.LookupSPF(context.Background(), "not a domain"); got.Complete {
		t.Errorf("invalid domain: %+v", got)
	}
}

// TestLookupSPFSystemOnly evaluates a record through the OS resolver (the
// SystemOnly mode), every name asked as an absolute name.
func TestLookupSPFSystemOnly(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp TXT", data("v=spf1 ip4:192.0.2.0/24 |a:web.example.jp mx"))
	s.resolve("web.example.jp A", data("203.0.113.7"))
	s.resolve("example.jp MX", data("mx1.example.jp"))
	s.resolve("mx1.example.jp A", data("198.51.100.25"))
	rec := &recordingResolver{r: goResolverTo(s)}
	c := New(nil, Config{Enabled: true, SystemOnly: true, Timeout: testTimeout * 4})
	c.sys = rec
	skipWithoutGoResolver(t, c, s)
	got := c.LookupSPF(context.Background(), "example.jp")
	want := SPFResult{Prefixes: prefixes("192.0.2.0/24", "203.0.113.7/32", "198.51.100.25/32"),
		Hosts: []string{"web.example.jp", "mx1.example.jp"}, Complete: true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LookupSPF = %+v, want %+v", got, want)
	}
	for _, n := range rec.asked() {
		if !strings.HasSuffix(n, ".") {
			t.Errorf("the OS resolver was asked the relative name %q", n)
		}
	}
}

// TestParseDualCIDR checks the CIDR lengths of a and mx terms.
func TestParseDualCIDR(t *testing.T) {
	for in, want := range map[string][3]int{
		"": {32, 128, 1}, "/24": {24, 128, 1}, "//64": {32, 64, 1}, "/24//64": {24, 64, 1}, "/0//0": {0, 0, 1},
		"/": {0, 0, 0}, "//": {0, 0, 0}, "/33": {0, 0, 0}, "//129": {0, 0, 0}, "/024": {0, 0, 0}, "24": {0, 0, 0}, "/24/64": {0, 0, 0},
	} {
		b4, b6, ok := parseDualCIDR(in)
		if ok != (want[2] == 1) || (ok && (b4 != want[0] || b6 != want[1])) {
			t.Errorf("parseDualCIDR(%q) = %d, %d, %v; want %v", in, b4, b6, ok, want)
		}
	}
}

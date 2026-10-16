package mailengine

import (
	"strings"
	"testing"
)

func TestParseDNSFailure(t *testing.T) {
	cases := []struct {
		name string
		diag string
		want DNSFailure
		ok   bool
	}{
		// Postfix, the forms seen in real notices.
		{"postfix try again", "Host or domain name not found. Name service error for name=infoseek.jp type=MX: Host not found, try again",
			DNSFailure{Name: "infoseek.jp", QType: "MX", Temporary: true}, true},
		{"postfix try again docomo", "Host or domain name not found. Name service error for name=docomo.ne.jp type=MX: Host not found, try again",
			DNSFailure{Name: "docomo.ne.jp", QType: "MX", Temporary: true}, true},
		{"postfix nxdomain", "550 5.1.2 <n@nowhere.example>: Host or domain name not found. Name service error for name=nowhere.example type=MX: Host not found",
			DNSFailure{Name: "nowhere.example", QType: "MX"}, true},
		{"postfix mx target", "Host or domain name not found. Name service error for name=mx01.example.co.jp type=A: Host not found, try again",
			DNSFailure{Name: "mx01.example.co.jp", QType: "A", Temporary: true}, true},
		{"postfix aaaa nxdomain", "Name service error for name=mx.example.net type=AAAA: Host not found",
			DNSFailure{Name: "mx.example.net", QType: "AAAA"}, true},
		{"postfix try again later", "Name service error for name=example.org type=MX: Host not found, try again later",
			DNSFailure{Name: "example.org", QType: "MX", Temporary: true}, true},
		{"postfix server failure", "Name service error for name=example.org type=MX: Server failure",
			DNSFailure{Name: "example.org", QType: "MX", Temporary: true}, true},
		{"postfix temporary failure", "Name service error for name=example.org type=MX: Temporary failure in name resolution",
			DNSFailure{Name: "example.org", QType: "MX", Temporary: true}, true},
		{"postfix timed out", "Name service error for name=example.org type=MX: DNS query timed out",
			DNSFailure{Name: "example.org", QType: "MX", Temporary: true}, true},
		{"upper case name and type", "NAME SERVICE ERROR FOR NAME=InfoSeek.JP. type=mx: HOST NOT FOUND, TRY AGAIN",
			DNSFailure{Name: "infoseek.jp", QType: "MX", Temporary: true}, true},
		{"folded line", "Host or domain name not found. Name service error for\r\n name=infoseek.jp type=MX: Host not found, try again",
			DNSFailure{Name: "infoseek.jp", QType: "MX", Temporary: true}, true},
		{"reason ends at the sentence", "Name service error for name=example.org type=MX: Host not found. Please try again later",
			DNSFailure{Name: "example.org", QType: "MX"}, true},

		// Sendmail: no record type.
		{"sendmail host unknown", "550 5.1.2 <a@nowhere.example>... Host unknown (Name server: nowhere.example.: host not found)",
			DNSFailure{Name: "nowhere.example"}, true},
		{"sendmail lookup failure", "Deferred: Name server: Example.COM.: host name lookup failure",
			DNSFailure{Name: "example.com", Temporary: true}, true},

		// Not detected.
		{"postfix nodata", "Host or domain name not found. Name service error for name=example.org type=MX: Host found but no data record of requested type",
			DNSFailure{}, false},
		{"postfix other reason", "Name service error for name=example.org type=MX: Malformed or unexpected name server reply",
			DNSFailure{}, false},
		{"postfix other type", "Name service error for name=example.org type=TXT: Host not found, try again",
			DNSFailure{}, false},
		{"no name", "Host or domain name not found. Host not found, try again",
			DNSFailure{}, false},
		{"empty", "", DNSFailure{}, false},
		{"unrelated", "550 5.1.1 <l@customer.example.com>: Recipient address rejected: User unknown in virtual mailbox table",
			DNSFailure{}, false},
		{"single label", "Name service error for name=mailhost type=A: Host not found",
			DNSFailure{}, false},
		{"ip literal", "Name service error for name=203.0.113.5 type=A: Host not found",
			DNSFailure{}, false},
		{"underscore", "Name service error for name=_25._tcp.mx.example.org type=MX: Host not found",
			DNSFailure{}, false},
		{"empty label", "Name service error for name=example..org type=MX: Host not found",
			DNSFailure{}, false},
		{"hyphen at label end", "Name service error for name=bad-.example.org type=MX: Host not found",
			DNSFailure{}, false},
		{"garbage characters", "Name service error for name=exa$mple.org type=MX: Host not found, try again",
			DNSFailure{}, false},
		{"label too long", "Name service error for name=" + strings.Repeat("a", 64) + ".example type=MX: Host not found",
			DNSFailure{}, false},
		{"name too long", "Name service error for name=" + strings.Repeat("abcdefghi.", 26) + "example type=MX: Host not found",
			DNSFailure{}, false},
		{"name of 253 characters", "Name service error for name=" + longName(61) + " type=MX: Host not found",
			DNSFailure{Name: longName(61), QType: "MX"}, true},
		{"name of 254 characters", "Name service error for name=" + longName(62) + " type=MX: Host not found",
			DNSFailure{}, false},
		{"sendmail garbage name", "Host unknown (Name server: <script>: host not found)",
			DNSFailure{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseDNSFailure(c.diag)
			if ok != c.ok || got != c.want {
				t.Errorf("ParseDNSFailure(%q) = %+v, %v; want %+v, %v", c.diag, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestDNSOutcomeVerified(t *testing.T) {
	for _, o := range []DNSOutcome{DNSOutcomeExists, DNSOutcomeNotFound, DNSOutcomeBroken} {
		if !o.verified() {
			t.Errorf("%q must count as verified", o)
		}
	}
	for _, o := range []DNSOutcome{"", "pending", "EXISTS"} {
		if o.verified() {
			t.Errorf("%q must count as not verified", o)
		}
	}
}

// longName returns a name of five labels ending in .jp whose first label
// has first characters: first + 192 characters long (61 gives 253).
func longName(first int) string {
	return strings.Repeat("a", first) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 60) + ".jp"
}

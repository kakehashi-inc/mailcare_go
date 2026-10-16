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

		// Postfix: a definite answer without the records (NODATA) and a reply
		// that could not be used (Postfix retries it).
		{"postfix nodata", "Host or domain name not found. Name service error for name=example.org type=MX: Host found but no data record of requested type",
			DNSFailure{Name: "example.org", QType: "MX"}, true},
		{"postfix nodata aaaa", "Host or domain name not found. Name service error for name=mx1.example.org type=AAAA: Host found but no data record of requested type",
			DNSFailure{Name: "mx1.example.org", QType: "AAAA"}, true},
		{"postfix non-recoverable", "Name service error for name=example.org type=MX: Non-recoverable error",
			DNSFailure{Name: "example.org", QType: "MX"}, true},
		{"postfix malformed reply", "Name service error for name=example.org type=MX: Malformed or unexpected name server reply",
			DNSFailure{Name: "example.org", QType: "MX", Temporary: true}, true},
		{"postfix null mx", "554 5.7.27 <u@nullmx.example>: Domain nullmx.example does not accept mail (nullMX)",
			DNSFailure{Name: "nullmx.example", QType: "MX"}, true},

		// Sendmail: the other resolver errors.
		{"sendmail non-recoverable", "Host unknown (Name server: example.net.: non-recoverable error)",
			DNSFailure{Name: "example.net"}, true},
		{"sendmail no data", "Host unknown (Name server: example.net.: no data known)",
			DNSFailure{Name: "example.net"}, true},
		{"sendmail null mx", "556 5.1.10 <u@example.net>... Host does not accept mail: MX 0 .",
			DNSFailure{}, true},

		// Not detected.
		{"postfix other type", "Name service error for name=example.org type=TXT: Host not found, try again",
			DNSFailure{}, false},
		{"postfix other reason", "Name service error for name=example.org type=MX: Connection refused",
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
			got, ok := ParseDNSFailure(c.diag, "")
			if ok != c.ok || got != c.want {
				t.Errorf("ParseDNSFailure(%q) = %+v, %v; want %+v, %v", c.diag, got, ok, c.want, c.ok)
			}
		})
	}
}

// TestParseDNSFailureMTAForms checks the DNS failure wordings of the major
// MTAs, as their sources and documentation write them (design document
// "mail classification" 4.4): a form that names the name gives it, a form
// without one means the recipient domain (Name ""), and the wording tells
// temporary from permanent.
func TestParseDNSFailureMTAForms(t *testing.T) {
	for _, c := range []struct {
		name string
		diag string
		want DNSFailure
	}{
		// Exim (src/routers/dnslookup.c, and the message of a domain no
		// router could route).
		{"exim host lookup did not complete", "retry timeout exceeded: host lookup did not complete",
			DNSFailure{Temporary: true}},
		{"exim missing mx", "all hosts for 'example.net' have been failing for a long time (and retry time not reached): missing MX, or all MXs point to missing A records, and defer_mode set",
			DNSFailure{Temporary: true}},
		{"exim mx to non-existent hosts", "all relevant MX records point to non-existent hosts",
			DNSFailure{}},
		{"exim no smtp service", "an MX or SRV record indicated no SMTP service",
			DNSFailure{}},
		// qmail.
		{"qmail no host named", "Sorry, I couldn't find any host named nowhere.example. (#5.1.2)",
			DNSFailure{Name: "nowhere.example"}},
		{"qmail no host by that name", "Sorry, I couldn't find any host by that name. (#4.1.2)",
			DNSFailure{Temporary: true}},
		{"qmail cname", "CNAME lookup failed temporarily. (#4.4.3)",
			DNSFailure{Temporary: true}},
		// Exchange / Microsoft 365.
		{"exchange domain does not exist", "550 5.4.310 DNS domain nowhere.example does not exist [Message=InfoDomainNonexistent] [LastAttemptedServerName=nowhere.example] [TYAP286MB1234.JPNP286.PROD.OUTLOOK.COM 2026-09-03T01:02:03.456Z 08DC0000000000]",
			DNSFailure{Name: "nowhere.example"}},
		{"exchange nonexistent domain", "550 5.4.310 SMTPSEND.DNS.NonExistentDomain; nonexistent domain",
			DNSFailure{}},
		{"exchange dns query failed", "450 4.4.312 DNS query failed. The error was: DNS query failed with error ErrorRetry",
			DNSFailure{Temporary: true}},
		// Gmail.
		{"gmail nxdomain", "DNS Error: 27848376 DNS type 'mx' lookup of nowhere.example responded with code NXDOMAIN Domain name not found: nowhere.example",
			DNSFailure{Name: "nowhere.example", QType: "MX"}},
		{"gmail servfail", "DNS Error: 9476340 DNS type 'mx' lookup of example.net responded with code SERVFAIL",
			DNSFailure{Name: "example.net", QType: "MX", Temporary: true}},
		{"gmail aaaa nodata", "DNS Error: 1234 DNS type 'aaaa' lookup of mx1.example.net responded with code NOERROR",
			DNSFailure{Name: "mx1.example.net", QType: "AAAA"}},
		{"gmail domain name not found", "DNS Error: Domain name not found",
			DNSFailure{}},
		{"gmail no data", "DNS Error: DNS server returned answer with no data",
			DNSFailure{}},
		{"gmail no dns servers", "DNS Error: Could not contact DNS servers",
			DNSFailure{Temporary: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseDNSFailure(c.diag, "")
			if !ok || got != c.want {
				t.Errorf("ParseDNSFailure(%q) = %+v, %v; want %+v, true", c.diag, got, ok, c.want)
			}
		})
	}
	// A typed form of a type no delivery looks up is not a delivery failure.
	if f, ok := ParseDNSFailure("DNS Error: 1234 DNS type 'txt' lookup of example.net responded with code SERVFAIL", ""); ok {
		t.Errorf("gmail txt: detected %+v", f)
	}
	// Exim's "Unrouteable address" is also its answer for an unknown local
	// user: no DNS failure, also in the reply of a remote Exim.
	for _, diag := range []string{
		"Unrouteable address",
		"SMTP error from remote mail server after RCPT TO:<nobody@example.net>: 550 Unrouteable address",
		"host mx1.example.net [192.0.2.25]: 550 Unrouteable address",
	} {
		if f, ok := ParseDNSFailure(diag, ""); ok {
			t.Errorf("%q: detected %+v", diag, f)
		}
	}
	// "DNS query failed" in the reply of a remote server is about the
	// lookups of that server: not a failure of the delivery to it.
	for _, c := range []struct{ diag, remote string }{
		{"host mx1.example.net[192.0.2.25] said: 451 4.4.312 DNS query failed (in reply to RCPT TO command)", ""},
		{"SMTP error from remote mail server after MAIL FROM:<a@example.jp>: 451 DNS query failed", ""},
		{"451 4.4.312 DNS query failed", "mx1.example.net"},
	} {
		if f, ok := ParseDNSFailure(c.diag, c.remote); ok {
			t.Errorf("%q (remote %q): detected %+v", c.diag, c.remote, f)
		}
	}
	// Exchange 4.4.316 is a refused connection, not a DNS failure.
	if f, ok := ParseDNSFailure("451 4.4.316 Connection refused [Message=Socket error code 10061]", ""); ok {
		t.Errorf("exchange 4.4.316: detected %+v", f)
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

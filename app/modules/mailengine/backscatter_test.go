package mailengine

import (
	"bytes"
	"context"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
)

// Received headers of returned originals (design document "mail
// classification" 2.6): a hop of forged mail from a host that is none of
// the sending sources, a hop whose only sign of the sending network is its
// address (the name is not registered), and a hop whose only sign of the
// sending server is its name.
const (
	spoofHop = "Received: from spam-sender.example (unknown [198.51.100.7])\r\n" +
		"\tby mx.partner.example.net (Postfix) with ESMTP id 4ZbCdE1fG2z9tLm\r\n" +
		"\tfor <nobody@partner.example.net>; Wed, 10 Sep 2025 03:12:44 +0900 (JST)\r\n"
	sourceHop = "Received: from app02.example.jp (app02.example.jp [203.0.113.5])\r\n" +
		"\tby mx.partner.example.net (Postfix) with ESMTPS id 4ZbCdE1fG2z9tLn\r\n" +
		"\tfor <nobody@partner.example.net>; Wed, 10 Sep 2025 03:12:44 +0900 (JST)\r\n"
	sourceNameHop = "Received: from mail.example.jp (unknown [198.51.100.99])\r\n" +
		"\tby mx.partner.example.net (Postfix) with ESMTPS id 4ZbCdE1fG2z9tLp\r\n" +
		"\tfor <nobody@partner.example.net>; Wed, 10 Sep 2025 03:12:44 +0900 (JST)\r\n"
	partnerHop = "Received: from mx.partner.example.net (localhost [127.0.0.1])\r\n" +
		"\tby mailstore.partner.example.net with LMTP id 8Kq2; Wed, 10 Sep 2025 03:12:45 +0900\r\n"
)

// returnedPart is the returned original attached as container
// (message/rfc822 or text/rfc822-headers) with the given headers before
// the headers of the original.
func returnedPart(container, headers string) string {
	part := "Content-Type: " + container + "\r\n\r\n" + headers +
		"From: Newsletter <newsletter@example.jp>\r\nTo: nobody@partner.example.net\r\nSubject: Invoice\r\n" +
		"Date: Wed, 10 Sep 2025 03:12:40 +0900\r\nMessage-ID: <inv-1@example.jp>\r\n"
	if container == "message/rfc822" {
		part += "\r\nPlease find the invoice.\r\n"
	}
	return part
}

// returnedNotice builds a Postfix report of a failed delivery from
// mx.partner.example.net to newsletter@example.jp that returns the original
// (see returnedPart).
func returnedNotice(container, headers string) []byte {
	const b = "=_bs"
	return []byte("From: MAILER-DAEMON@mx.partner.example.net (Mail Delivery System)\r\n" +
		"To: newsletter@example.jp\r\n" +
		"Subject: Undelivered Mail Returned to Sender\r\n" +
		"Date: Wed, 10 Sep 2025 03:13:01 +0900\r\n" +
		"Auto-Submitted: auto-replied\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/report; report-type=delivery-status; boundary=\"" + b + "\"\r\n\r\n" +
		"--" + b + "\r\nContent-Type: text/plain; charset=us-ascii\r\n\r\n" +
		"This is the mail system at host mx.partner.example.net.\r\n\r\n" +
		"<nobody@partner.example.net>: unknown user: \"nobody\"\r\n" +
		"\r\n--" + b + "\r\nContent-Type: message/delivery-status\r\n\r\n" +
		"Reporting-MTA: dns; mx.partner.example.net\r\n\r\n" +
		"Final-Recipient: rfc822; nobody@partner.example.net\r\n" +
		"Action: failed\r\nStatus: 5.1.1\r\n" +
		"Diagnostic-Code: X-Postfix; unknown user: \"nobody\"\r\n" +
		"\r\n--" + b + "\r\n" + returnedPart(container, headers) +
		"\r\n--" + b + "--\r\n")
}

func TestOriginalReceived(t *testing.T) {
	hops := "Received: from mx.partner.example.net (localhost [127.0.0.1])\r\n\tby mailstore.partner.example.net with LMTP;\r\n" +
		"\tWed, 10 Sep 2025 03:12:45 +0900\r\n" +
		"Received: from mail.example.jp (mail.example.jp [203.0.113.5])\r\n" +
		"        by mx.partner.example.net (Postfix) with ESMTPS id 4ZbCdE1fG2z9tLn\r\n" +
		"Received: by app01.example.jp (Postfix, from userid 1001) id 5A1B2C3D4E\r\n"
	want := []string{
		"from mx.partner.example.net (localhost [127.0.0.1]) by mailstore.partner.example.net with LMTP; Wed, 10 Sep 2025 03:12:45 +0900",
		"from mail.example.jp (mail.example.jp [203.0.113.5]) by mx.partner.example.net (Postfix) with ESMTPS id 4ZbCdE1fG2z9tLn",
		"by app01.example.jp (Postfix, from userid 1001) id 5A1B2C3D4E",
	}
	for _, container := range []string{"message/rfc822", "text/rfc822-headers"} {
		t.Run(container, func(t *testing.T) {
			om := ParseMessage(returnedNotice(container, hops)).OriginalMessage
			if om == nil {
				t.Fatal("no original message")
			}
			if strings.Join(om.Received, "\n") != strings.Join(want, "\n") {
				t.Errorf("Received = %q, want %q", om.Received, want)
			}
			if om.Subject != "Invoice" {
				t.Errorf("subject = %q: the headers after Received were not read", om.Subject)
			}
			if om := ParseMessage(returnedNotice(container, "")).OriginalMessage; om == nil || len(om.Received) != 0 {
				t.Errorf("without Received: %+v", om)
			}
		})
	}
	// The Received headers of the notice are not the original's.
	pm := ParseMessage(readSample(t, "postfix_backscatter.eml"))
	if pm.OriginalMessage == nil || len(pm.OriginalMessage.Received) != 1 ||
		!strings.HasPrefix(pm.OriginalMessage.Received[0], "from spam-sender.example (unknown [198.51.100.7]) by mx.partner.example.net") {
		t.Errorf("sample original Received = %+v", pm.OriginalMessage)
	}
	if pm := ParseMessage(readSample(t, "normal.eml")); pm.OriginalMessage != nil {
		t.Errorf("a mail without an attached original has one: %+v", pm.OriginalMessage)
	}
}

func TestMatchReceived(t *testing.T) {
	source4 := NewSendingSources([]string{"203.0.113.0/24"}, nil, nil, nil)
	source6 := NewSendingSources(nil, []string{"2001:db8::/32"}, nil, nil)
	hosts := NewSendingSources(nil, nil, []string{"mail.example.jp", "mailhost"}, nil)
	all := NewSendingSources([]string{"203.0.113.0/24"}, []string{"2001:db8::/32"}, []string{"mail.example.jp", "mailhost"}, nil)
	const postfix = "from mail.example.jp (mail.example.jp [203.0.113.5]) by mx.partner.example.net (Postfix) with ESMTPS id 4ZbCdE1fG2z9tLn " +
		"for <taro@partner.example.net>; Wed, 10 Sep 2025 03:12:44 +0900 (JST)"
	cases := []struct {
		name    string
		sources SendingSources
		line    string
		want    bool
	}{
		{"postfix, by address", source4, postfix, true},
		{"postfix, by name", hosts, postfix, true},
		{"postfix, nothing configured", SendingSources{}, postfix, false},
		{"postfix, foreign client", all,
			"from spam-sender.example (unknown [198.51.100.7]) by mx.partner.example.net (Postfix) with ESMTP id 4ZbC; Wed, 10 Sep 2025 03:12:44 +0900", false},
		{"postfix, client address only", source4, "from relay.other.example (unknown [203.0.113.77]) by mx.partner.example.net (Postfix) with ESMTP id 4Z", true},
		{"gmail, IPv6 client with a dotted name", source6,
			"from mail.other.example (mail.other.example. [2001:db8::25]) by mx.google.com with ESMTPS id d9443c01a7336-2a1b " +
				"for <user2@gmail.com> (version=TLS1_3 cipher=TLS_AES_256_GCM_SHA384 bits=256/256); Sun, 07 Sep 2025 00:59:51 -0700 (PDT)", true},
		{"gmail, bare IPv6 after by", source6, "by 2001:db8::1 with SMTP id m5csp1234567; Sun, 7 Sep 2025 00:59:52 -0700 (PDT)", true},
		{"IPv6 tag in parentheses", source6, "from relay.other.example (IPv6:2001:db8::25) by mx.partner.example.net with ESMTP", true},
		{"IPv6 tag in brackets", source6, "from relay.other.example (unknown [IPv6:2001:db8::25]) by mx.partner.example.net with ESMTP", true},
		{"IPv4-mapped address", source4, "from relay.other.example ([::ffff:203.0.113.5]) by mx.partner.example.net with LMTP id x", true},
		{"exchange, bare address in parentheses", source4,
			"from EXCH01.corp.other.example (203.0.113.30) by mx.partner.example.net with Microsoft SMTP Server id 15.20.8205.10; Tue, 9 Sep 2025 10:00:00 +0900", true},
		{"exchange, bare IPv6 in parentheses", source6,
			"from JPXP123MB4567.JPNP123.PROD.OUTLOOK.COM (2001:db8:400:1::10) by JPXP123MB9999.JPNP123.PROD.OUTLOOK.COM (2603:1096:400::20) with Microsoft SMTP Server", true},
		{"address with a port in parentheses", source4, "from unknown (HELO relay) (203.0.113.5:41234) by mx.partner.example.net with SMTP", true},
		{"bare address after from", source4, "from 203.0.113.5 by mx.partner.example.net with SMTP", true},
		{"host name, case and trailing dot", hosts, "from MAIL.Example.JP. (unknown [198.51.100.7]) by mx.partner.example.net with ESMTP", true},
		{"host name after by", hosts, "from app01.other.example (app01.other.example [198.51.100.20]) by mail.example.jp (Postfix) with ESMTP id 4Xk1", true},
		{"host name looked up by the receiver", hosts, "from localhost.localdomain (mail.example.jp [198.51.100.7]) by mx.partner.example.net with ESMTP", true},
		{"exim helo name", hosts, "from [198.51.100.7] (port=41234 helo=mail.example.jp) by mail.partner.example.net with esmtpsa (TLS1.3) (Exim 4.96)", true},
		{"qmail HELO name, single label", hosts, "from unknown (HELO mailhost) (198.51.100.7) by mx.partner.example.net with SMTP; 5 Sep 2025 02:29:58 -0000", true},
		{"host names only, the address is not looked up", hosts, "from relay.other.example (relay.other.example [203.0.113.5]) by mx.partner.example.net", false},
		{"look-alike host names", hosts, "from mail.example.jp.evil.example (notmail.example.jp [198.51.100.7]) by mx.mail-example.jp", false},
		{"IPv4 configured, IPv6 seen", source4, "from relay.other.example (unknown [2001:db8::25]) by mx.partner.example.net", false},
		{"IPv6 configured, IPv4 seen", source6, "from relay.other.example (unknown [203.0.113.5]) by mx.partner.example.net", false},
		{"address outside the hop positions", source4, "from spam-sender.example (unknown [198.51.100.7]) by mx.partner.example.net with ESMTP id 203.0.113.5", false},
		{"names inside other comments", hosts, "from spam-sender.example (unknown [198.51.100.7]) by mx.partner.example.net (mailhost) with ESMTP", false},
		{"qmail without a hop", all, "(qmail 12300 invoked by uid 1001); 5 Sep 2025 02:29:58 -0000", false},
		{"qmail from network", all, "(qmail 4711 invoked from network); 5 Sep 2025 02:29:58 -0000", false},
		{"empty", all, "", false},
		{"blank", all, "  \t ", false},
		{"keywords only", all, "from by", false},
		{"unbalanced", all, "from ((( [ ]]] ) by )) [IPv6: (", false},
		{"garbage", all, "\x00\xff[\x01]((;;,,)) from \xfe by [::ffff:]", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.sources.MatchReceived(c.line); got != c.want {
				addrs, hosts := receivedSources(c.line)
				t.Errorf("MatchReceived(%q) = %v, want %v (addresses %v, names %q)", c.line, got, c.want, addrs, hosts)
			}
		})
	}
}

// bracketedHops returns a Received header from a host that is no sending source, with n
// distinct IPv4 literals in brackets (and each repeated `repeat` times).
func bracketedHops(n, repeat int) string {
	var b strings.Builder
	b.WriteString("from spam-sender.example (unknown")
	for i := range n {
		for range repeat {
			fmt.Fprintf(&b, " [198.51.%d.%d]", 100+i/250, i%250+1)
		}
	}
	b.WriteString(") by mx.partner.example.net (Postfix) with ESMTP id 4ZbC")
	return b.String()
}

// TestReceivedNoBounds: every Received header of an original is judged,
// however many there are, however long each is and however many addresses
// it names: a sending source after thousands of other addresses, past the
// hundredth header or deep into a long header still shows the mail was sent
// from the sending sources, and an original without one is backscatter.
func TestReceivedNoBounds(t *testing.T) {
	sources := NewSendingSources([]string{"203.0.113.0/24"}, nil, []string{"mail.example.jp"}, nil)
	const hop = "from spam-sender.example (unknown [198.51.100.7]) by mx.partner.example.net (Postfix) with ESMTP"
	const source = "from mail.example.jp (mail.example.jp [203.0.113.5]) by mx.partner.example.net (Postfix) with ESMTP"
	long := hop + " id " + strings.Repeat("x", 64<<10) + " [203.0.113.5]"
	for _, c := range []struct {
		name  string
		lines []string
		match bool
	}{
		{"5000 distinct addresses, then a source", []string{bracketedHops(5000, 1) + " [203.0.113.5]"}, true},
		{"5000 distinct addresses, each three times", []string{bracketedHops(5000, 3)}, false},
		{"a source after 64 KiB", []string{long}, true},
		{"a source in the 1000th header", append(slices.Repeat([]string{hop}, 999), source), true},
		{"1000 headers without a source", slices.Repeat([]string{hop}, 1000), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := sources.matchReceivedLines(c.lines); got != c.match {
				t.Errorf("matchReceivedLines = %v, want %v", got, c.match)
			}
			if got := sources.MatchReceived(c.lines[len(c.lines)-1]); len(c.lines) == 1 && got != c.match {
				t.Errorf("MatchReceived = %v, want %v", got, c.match)
			}
			var headers strings.Builder
			for _, l := range c.lines {
				headers.WriteString("Received: " + l + "\r\n")
			}
			cls := ClassifyWith(ParseMessage(returnedNotice("message/rfc822", headers.String())), "newsletter@example.jp", sources)
			want := ruleBackscatter
			if c.match {
				want = ruleDSNReport
			}
			if cls.Rule != want {
				t.Errorf("rule %q, want %q", cls.Rule, want)
			}
		})
	}
}

// hugeHop returns a Received header of at least size bytes of bracketed
// addresses, none of them a sending source.
func hugeHop(size int) string {
	var b strings.Builder
	b.WriteString("from spam-sender.example (unknown")
	for b.Len() < size {
		fmt.Fprintf(&b, " [198.51.%d.%d]", b.Len()%250, b.Len()%251)
	}
	b.WriteString(") by mx.partner.example.net")
	return b.String()
}

// TestReceivedHostileOriginalIsLinear: a header is read in one pass and
// every address is looked up among the prefixes by their lengths (addrIndex),
// so 100000 prefixes (SPF records can list many ranges) and a header of 4 MB
// of bracketed addresses are judged in about the time of reading the header,
// not in the product of both; so are an original with a header of almost
// 1 MB (the most the parser reads) and one with thousands of short
// headers. None of them shows a sending source, so the backscatter rule
// takes them. The time limit is far above the linear cost (also under the
// race detector) and far below the cost of comparing every address with
// every prefix.
func TestReceivedHostileOriginalIsLinear(t *testing.T) {
	var prefixes []netip.Prefix
	for i := range 100000 {
		bits := 24
		if i%2 == 1 {
			bits = 32
		}
		prefixes = append(prefixes, netip.PrefixFrom(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), bits).Masked())
	}
	sources := NewSendingSources(nil, nil, []string{"mail.example.jp"}, nil).withResolved(nil, prefixes, nil)
	line := hugeHop(4 << 20)
	start := time.Now()
	matched := sources.matchReceivedLines([]string{line})
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("a 4 MB header judged in %v", elapsed)
	}
	if matched {
		t.Error("a 4 MB header without a sending source matched")
	}
	if !sources.MatchReceived(hugeHop(1<<20) + " [10.0.1.7]") {
		t.Error("a sending source after 1 MB of addresses was not found")
	}
	big := "Received: " + hugeHop(1<<20-4096) + "\r\n"
	many := strings.Repeat("Received: from spam-sender.example (unknown [198.51.100.7]) by mx.partner.example.net\r\n", 5000)
	for name, headers := range map[string]string{"1 MB header": big, "5000 headers": many} {
		pm := ParseMessage(returnedNotice("message/rfc822", headers))
		if pm.OriginalMessage == nil || len(pm.OriginalMessage.Received) == 0 {
			t.Fatalf("%s: the original was not read", name)
		}
		start := time.Now()
		cls := ClassifyWith(pm, "newsletter@example.jp", sources)
		if elapsed := time.Since(start); elapsed > 20*time.Second {
			t.Errorf("%s: classified in %v", name, elapsed)
		}
		if cls.Rule != ruleBackscatter {
			t.Errorf("%s: rule %q, want %q", name, cls.Rule, ruleBackscatter)
		}
	}
}

// TestAddrIndex: the index finds an address in a prefix of any length of
// the same family, among the single addresses, and never across families.
func TestAddrIndex(t *testing.T) {
	x := newAddrIndex([]netip.Prefix{
		netip.MustParsePrefix("203.0.113.77/24"), // unmasked on purpose
		netip.MustParsePrefix("198.51.100.8/29"),
		netip.MustParsePrefix("2001:db8:1::/48"),
		netip.MustParsePrefix("::ffff:192.0.2.0/120"),
	}, []netip.Addr{netip.MustParseAddr("192.0.2.200"), netip.MustParseAddr("2001:db8:9::1")})
	for addr, want := range map[string]bool{
		"203.0.113.1":   true,
		"203.0.114.1":   false,
		"198.51.100.15": true,
		"198.51.100.16": false,
		"2001:db8:1::5": true,
		"2001:db8:2::5": false,
		"192.0.2.1":     false, // only an IPv4-mapped prefix covers it
		"192.0.2.200":   true,
		"2001:db8:9::1": true,
		"2001:db8:9::2": false,
	} {
		if got := x.contains(netip.MustParseAddr(addr)); got != want {
			t.Errorf("contains(%s) = %v, want %v", addr, got, want)
		}
	}
	if (addrIndex{}).contains(netip.MustParseAddr("192.0.2.1")) {
		t.Error("the empty index matched")
	}
}

// TestMatchReceivedNothingConfigured: without an entry in any of the three
// settings nothing matches, not even an address handed to NewSendingSources as
// already resolved (resolved addresses alone are no configuration), nor the
// host name of a blank entry.
func TestMatchReceivedNothingConfigured(t *testing.T) {
	const line = "from mail.example.jp (mail.example.jp [203.0.113.5]) by mx.partner.example.net (Postfix) with ESMTPS id 4Zb"
	sources := NewSendingSources([]string{" "}, []string{""}, []string{" "}, addrs("203.0.113.5"))
	if !sources.Empty() {
		t.Fatal("blank entries and resolved addresses count as configured")
	}
	if sources.MatchReceived(line) {
		t.Errorf("MatchReceived(%q) = true with nothing configured", line)
	}
	// The same resolved address matches once an entry is configured.
	if !NewSendingSources(nil, nil, []string{"other.example.jp"}, addrs("203.0.113.5")).MatchReceived(line) {
		t.Error("the resolved address does not match with a host name configured")
	}
}

// TestClassifyBackscatter covers the backscatter rule (design document "mail
// classification" 2.6): the return notice of a message none of the sending
// sources handed on is junk, and the notices about mail from the sending
// sources (shown in the Received headers or reported by a sending server),
// the notices without a returned original or its Received headers, the
// other reports (DMARC, read receipts), the automatic replies, the forwards
// of a person and every message without a source host name configured keep
// their classification.
func TestClassifyBackscatter(t *testing.T) {
	const mailbox = "newsletter@example.jp"
	sourceNet := NewSendingSources([]string{"203.0.113.0/24"}, nil, []string{"mail.example.jp"}, nil)
	sourceAll := NewSendingSources([]string{"203.0.113.0/24"}, []string{"2001:db8::/32"}, []string{"mail.example.jp"}, nil)
	sourceHosts := NewSendingSources(nil, nil, []string{"mail.example.jp"}, nil)
	sourceAddrs := NewSendingSources([]string{"203.0.113.0/24"}, []string{"2001:db8::/32"}, nil, nil)
	sourceMTA := NewSendingSources(nil, nil, []string{"mx1.example.jp"}, nil)
	elsewhere := NewSendingSources([]string{"192.0.2.250"}, nil, []string{"elsewhere.example.jp"}, nil)
	rfc822 := func(headers string) []byte { return returnedNotice("message/rfc822", headers) }
	// The delayed warning of a sending server (mx1.example.jp), returning the
	// headers of the sent mail with the hop from the application server to
	// the MTA, and the same warning about mail whose hops name none of the
	// source hosts (submitted through a server that is not registered).
	delayedWith := func(received string) []byte {
		return bytes.Replace(readSample(t, "postfix_delayed_dns.eml"), []byte("From: Newsletter <newsletter@example.jp>\n"),
			[]byte(received+"From: Newsletter <newsletter@example.jp>\n"), 1)
	}
	sourceDelayed := delayedWith("Received: from app01.example.jp (app01.example.jp [192.0.2.20])\n\tby mx1.example.jp (Postfix) with ESMTP id 4Xk1Yy6Q8Fz9tLn\n" +
		"\tfor <user1@infoseek.jp>; Wed, 3 Sep 2025 08:59:01 +0900 (JST)\n")
	sourceDelayedForeignHops := delayedWith("Received: from laptop.home.example (unknown [198.51.100.7])\n" +
		"\tby submit.other.example (Postfix) with ESMTPSA id 4Xk1Yy6Q8Fz9tLm; Wed, 3 Sep 2025 08:59:01 +0900 (JST)\n")
	// A notice whose reporting MTA is an address of the sending network.
	reportedByAddress := bytes.Replace(dnsNotice(bounceKindFailed, "5.4.4", "n@nowhere.example", "[203.0.113.25]",
		"Name service error for name=nowhere.example type=MX: Host not found"),
		[]byte("From: Newsletter <newsletter@example.jp>\r\n"), []byte(spoofHop+"From: Newsletter <newsletter@example.jp>\r\n"), 1)
	notice := "From: postmaster@mx.partner.example.net\r\nTo: newsletter@example.jp\r\nSubject: Undeliverable: Invoice\r\n"
	wording := "Delivery has failed to these recipients or groups:\r\nnobody@partner.example.net"
	relay := "From: Notifications <notify@relay.partner.example.net>\r\nTo: newsletter@example.jp\r\nSubject: Message notification\r\n"
	person := "From: Hanako <hanako@partner.example.com>\r\nTo: newsletter@example.jp\r\n"
	oof := person + "Auto-Submitted: auto-replied\r\n"
	report := gzipped(t, dmarcXML(recSPFMissing))
	cases := []struct {
		name    string
		raw     []byte
		sources SendingSources
		mailbox string // "" = mailbox; "-" = unknown
		kind    string
		rule    string
		// foreign, when set, is a configuration under which the same message
		// is backscatter: the case reaches the rule and is spared only by
		// what it tests.
		foreign SendingSources
		// without is the rule of a backscatter case without sending sources
		// ("" = dsn_report, "-" = ordinary mail).
		without string
	}{
		// The return notices of forged mail, reported by the receiver's MTA.
		{name: "spoofed postfix sample", raw: readSample(t, "postfix_backscatter.eml"), sources: sourceNet, kind: bounceKindJunk, rule: ruleBackscatter},
		{name: "spoofed, original attached whole", raw: rfc822(spoofHop), sources: sourceNet, kind: bounceKindJunk, rule: ruleBackscatter},
		{name: "spoofed, original headers only", raw: returnedNotice("text/rfc822-headers", spoofHop), sources: sourceNet, kind: bounceKindJunk, rule: ruleBackscatter},
		{name: "spoofed, several hops", raw: rfc822(partnerHop + spoofHop), sources: sourceAll, kind: bounceKindJunk, rule: ruleBackscatter},
		{name: "spoofed, host names configured", raw: rfc822(spoofHop), sources: sourceHosts, kind: bounceKindJunk, rule: ruleBackscatter},
		{name: "spoofed, the sending MTA configured", raw: rfc822(spoofHop), sources: sourceMTA, kind: bounceKindJunk, rule: ruleBackscatter},
		{name: "spoofed, signed by another domain", raw: rfc822("DKIM-Signature: v=1; a=rsa-sha256; d=spam-sender.example; s=s1; bh=x; b=y\r\n" + spoofHop),
			sources: sourceNet, kind: bounceKindJunk, rule: ruleBackscatter},
		{name: "spoofed, signed with the monitored domain, monitored domain unknown",
			raw:     rfc822("DKIM-Signature: v=1; a=rsa-sha256; d=example.jp; s=s1; bh=x; b=y\r\n" + spoofHop),
			sources: sourceNet, mailbox: "-", kind: bounceKindJunk, rule: ruleBackscatter},
		// Only an address is shown for a host registered by its name: the
		// name is not looked up (a documented limit: register the addresses
		// too).
		{name: "host names configured, hop shown by its address only",
			raw:     rfc822("Received: from relay.other.example (relay.other.example [203.0.113.5]) by mx.partner.example.net with ESMTP\r\n"),
			sources: sourceHosts, kind: bounceKindJunk, rule: ruleBackscatter},
		{name: "notice without delivery-status, by the subject and the sender", sources: sourceNet, kind: bounceKindJunk, rule: ruleBackscatter, without: ruleDaemonSender,
			raw: multipartMessage(notice, "mixed", textPart(wording), returnedPart("message/rfc822", spoofHop))},
		{name: "notice without delivery-status, by the HTML wording only", sources: sourceNet, kind: bounceKindJunk, rule: ruleBackscatter, without: ruleBodyPattern,
			raw: multipartMessage(relay, "mixed", textPart("A new notification is available."), htmlPart("<p>"+wording+"</p>"),
				returnedPart("message/rfc822", spoofHop))},
		// A forward prefix does not spare a daemon's mail.
		{name: "forward prefix from a daemon address", sources: sourceNet, kind: bounceKindJunk, rule: ruleBackscatter, without: ruleDaemonSender,
			raw: multipartMessage("From: MAILER-DAEMON@relay.partner.example.net\r\nTo: newsletter@example.jp\r\nSubject: Fwd: Undeliverable: Invoice\r\n",
				"mixed", textPart(wording), returnedPart("message/rfc822", spoofHop))},

		// Without a source host name nothing is judged: a sending server names
		// itself in the hops of the sent mail, the addresses there are the peers'.
		{name: "addresses only configured", raw: rfc822(spoofHop), sources: sourceAddrs, kind: bounceKindFailed, rule: ruleDSNReport, foreign: sourceNet},
		{name: "delayed warning of the sending MTA, only the MTA's address configured", raw: sourceDelayed,
			sources: NewSendingSources([]string{"192.0.2.10"}, nil, nil, nil), kind: bounceKindDelayed, rule: ruleDSNReport, foreign: elsewhere},

		// The notices about mail from the sending sources.
		{name: "source hop by address", raw: rfc822(sourceHop), sources: sourceNet, kind: bounceKindFailed, rule: ruleDSNReport, foreign: elsewhere},
		{name: "source hop by name", raw: rfc822(sourceNameHop), sources: sourceAll, kind: bounceKindFailed, rule: ruleDSNReport, foreign: elsewhere},
		{name: "source hop by name, host names configured", raw: rfc822(sourceNameHop), sources: sourceHosts, kind: bounceKindFailed, rule: ruleDSNReport, foreign: elsewhere},
		{name: "source hop below the receiver's hops", raw: rfc822(partnerHop + sourceHop), sources: sourceNet, kind: bounceKindFailed, rule: ruleDSNReport, foreign: elsewhere},
		{name: "source hop by IPv6", sources: sourceAll, kind: bounceKindFailed, rule: ruleDSNReport, foreign: elsewhere,
			raw: rfc822("Received: from mail6.other.example (mail6.other.example [IPv6:2001:db8::25]) by mx.partner.example.net with ESMTPS\r\n")},
		{name: "signed with the monitored domain", raw: rfc822("DKIM-Signature: v=1; a=rsa-sha256; d=example.jp; s=s1; bh=x; b=y\r\n" + spoofHop),
			sources: sourceNet, kind: bounceKindFailed, rule: ruleDSNReport},
		{name: "signed with a subdomain of the monitored domain", raw: rfc822("DKIM-Signature: v=1; a=rsa-sha256; d=Mail.Example.JP; s=s1; bh=x; b=y\r\n" + spoofHop),
			sources: sourceNet, kind: bounceKindFailed, rule: ruleDSNReport},
		{name: "delayed warning of the sending MTA", raw: sourceDelayed, sources: sourceMTA, kind: bounceKindDelayed, rule: ruleDSNReport, foreign: elsewhere},
		// A sending server reports about mail it handled, whatever the hops say.
		{name: "delayed warning of the sending MTA, hops naming no sending source", raw: sourceDelayedForeignHops,
			sources: sourceMTA, kind: bounceKindDelayed, rule: ruleDSNReport, foreign: elsewhere},
		{name: "reported by an address of the sending network", raw: reportedByAddress, sources: sourceNet, kind: bounceKindFailed, rule: ruleDSNReport, foreign: elsewhere},
		{name: "delayed warning sample without Received", raw: readSample(t, "postfix_delayed_dns.eml"),
			sources: sourceMTA, kind: bounceKindDelayed, rule: ruleDSNReport},

		// Nothing to judge.
		{name: "nothing configured", raw: readSample(t, "postfix_backscatter.eml"), kind: bounceKindFailed, rule: ruleDSNReport, foreign: sourceNet},
		{name: "original without Received", raw: rfc822(""), sources: sourceNet, kind: bounceKindFailed, rule: ruleDSNReport},
		{name: "original headers without Received", raw: returnedNotice("text/rfc822-headers", ""), sources: sourceNet, kind: bounceKindFailed, rule: ruleDSNReport},
		{name: "no original attached", raw: dsnMessage("Undelivered Mail Returned to Sender", "Action: failed\r\nStatus: 5.1.1\r\n"),
			sources: sourceNet, kind: bounceKindFailed, rule: ruleDSNReport},
		{name: "DMARC aggregate report", raw: dmarcMail("application/gzip", "r.xml.gz", report), mailbox: "dmarc@example.jp",
			sources: sourceNet, kind: bounceKindReport, rule: ruleDMARCReport},
		// A read receipt is a report of another type, never a return notice.
		{name: "read receipt of forged mail", sources: sourceNet, kind: "", rule: "",
			raw: multipartMessage("From: Taro <nobody@partner.example.net>\r\nTo: newsletter@example.jp\r\nSubject: Read: Invoice\r\n",
				"report; report-type=disposition-notification", textPart("The message was displayed."),
				"Content-Type: message/disposition-notification\r\n\r\nReporting-UA: partner.example.net; Mailer\r\n"+
					"Final-Recipient: rfc822; nobody@partner.example.net\r\nDisposition: manual-action/MDN-sent-manually; displayed\r\n",
				returnedPart("text/rfc822-headers", spoofHop))},
		{name: "read receipt that looks like a notice", sources: sourceNet, kind: bounceKindFailed, rule: ruleDaemonSender,
			raw: multipartMessage("From: postmaster@partner.example.net\r\nTo: newsletter@example.jp\r\nSubject: Read: Undeliverable: Invoice\r\n",
				"report; report-type=disposition-notification", textPart("The message was displayed."),
				"Content-Type: message/disposition-notification\r\n\r\nReporting-UA: partner.example.net; Mailer\r\n"+
					"Final-Recipient: rfc822; nobody@partner.example.net\r\nDisposition: automatic-action/MDN-sent-automatically; displayed\r\n",
				returnedPart("text/rfc822-headers", spoofHop))},
		{name: "automatic reply sample", raw: readSample(t, "autoreply.eml"), sources: sourceNet, kind: bounceKindAutoReply, rule: ruleAutoReply},
		{name: "out-of-office reply with the original attached", sources: sourceNet, kind: bounceKindAutoReply, rule: ruleAutoReply,
			raw: multipartMessage(oof+"Subject: Automatic reply: Invoice\r\n", "mixed",
				textPart("Out of the office until Monday."), returnedPart("message/rfc822", spoofHop))},
		// An automatic reply quoting a bounce subject looks like a notice by
		// its subject, but the out-of-office subject spares it.
		{name: "automatic reply quoting a bounce subject, forged original", sources: sourceNet, kind: bounceKindAutoReply, rule: ruleAutoReply,
			raw: multipartMessage(oof+"Subject: Automatic reply: Undeliverable: Invoice\r\n", "mixed",
				textPart("Out of the office."), returnedPart("message/rfc822", spoofHop))},
		{name: "out-of-office subject quoting a bounce subject, no Auto-Submitted", sources: sourceNet, kind: bounceKindAutoReply, rule: ruleAutoReplySubject,
			raw: multipartMessage(person+"Subject: Out of Office: Undeliverable: Invoice\r\n", "mixed",
				textPart("Out of the office."), returnedPart("message/rfc822", spoofHop))},
		// A person forwarding a notice of forged mail with the notice attached.
		{name: "forward of a person", sources: sourceNet, kind: bounceKindFailed, rule: ruleSubjectPattern,
			raw: multipartMessage(person+"Subject: Fwd: Undeliverable: Invoice\r\n", "mixed",
				textPart("Is this genuine?"), returnedPart("message/rfc822", spoofHop))},
		// A person forwarding a notice with the report wording quoted: it
		// looks like a notice, and the forward prefix spares it.
		{name: "forward of a person quoting the report wording", sources: sourceNet, kind: bounceKindFailed, rule: ruleSubjectPattern,
			raw: multipartMessage(person+"Subject: Fwd: Undeliverable: Invoice\r\n", "mixed",
				textPart("Is this genuine?\r\n> "+wording), returnedPart("message/rfc822", spoofHop))},
		{name: "a forwarded message is no notice", sources: sourceNet, kind: "", rule: "",
			raw: multipartMessage("From: Camille <camille@partner.example.fr>\r\nTo: newsletter@example.jp\r\nSubject: Fwd: Invoice\r\n", "mixed",
				textPart("Is this genuine?"), returnedPart("message/rfc822", spoofHop))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mb := mailbox
			switch c.mailbox {
			case "":
			case "-":
				mb = ""
			default:
				mb = c.mailbox
			}
			pm := ParseMessage(c.raw)
			cls := ClassifyWith(pm, mb, c.sources)
			if cls.Kind != c.kind || cls.Rule != c.rule || cls.IsBounce != (c.kind != bounceKindJunk && c.kind != "") {
				t.Fatalf("classification = %+v, want kind %q rule %q", cls, c.kind, c.rule)
			}
			plain := Classify(pm, mb)
			if plain.Rule == ruleBackscatter {
				t.Error("Classify without sending sources judged backscatter")
			}
			if !c.foreign.Empty() {
				if got := ClassifyWith(pm, mb, c.foreign); got.Rule != ruleBackscatter {
					t.Errorf("with a foreign configuration = %+v: the case does not reach the rule", got)
				}
			}
			if c.rule != ruleBackscatter {
				return
			}
			switch c.without {
			case "":
				c.without = ruleDSNReport
			case "-":
				c.without = ""
			}
			if plain.Rule != c.without {
				t.Errorf("without sending sources = %+v, want rule %q", plain, c.without)
			}
			// The rule reads no body: both passes give the same
			// answer, and the first decides.
			id := newMailboxIdentity(mb)
			id.sources = c.sources
			for _, body := range []string{"", pm.bodyForClassification(), pm.secondaryBody(), "Delayed: 4.4.1"} {
				if got, ok := classifyWith(pm, body, id); !ok || got.Rule != ruleBackscatter {
					t.Errorf("pass with body %q = %+v", body, got)
				}
			}
			if cls.BodySource != pm.primarySource() {
				t.Errorf("body_source = %q, want the primary %q", cls.BodySource, pm.primarySource())
			}
		})
	}

	// A DMARC aggregate report is never a return notice, even one that
	// presents itself as a daemon's mail and attaches a forged original.
	reportWithOriginal := bytes.Replace(bytes.Replace(dmarcMail("application/gzip", "r.xml.gz", report),
		[]byte("From: noreply-dmarc@receiver.example.net"), []byte("From: MAILER-DAEMON@receiver.example.net"), 1),
		[]byte("\r\n--b1--\r\n"), []byte("\r\n--b1\r\n"+returnedPart("message/rfc822", spoofHop)+"\r\n--b1--\r\n"), 1)
	pm := ParseMessage(reportWithOriginal)
	if cls := ClassifyWith(pm, "dmarc@example.jp", sourceNet); cls.Rule != ruleDMARCReport {
		t.Errorf("DMARC report with a forged original = %+v, want the report", cls)
	}
	pm.DMARC = nil
	if id := newMailboxIdentity("dmarc@example.jp"); !isBackscatter(pm, mailboxIdentity{domain: id.domain, sources: sourceNet}) {
		t.Error("the DMARC case does not reach the rule without its report")
	}
}

// TestBackscatterNoticeLookAlike: a return notice without a delivery-status
// part counts only when it looks like a notice (looksLikeNotice): a mail
// delivery system display name together with a bounce subject does, either
// alone does not. A multipart/report without a report-type is not taken for
// a report of another type, one of the type disposition-notification is.
func TestBackscatterNoticeLookAlike(t *testing.T) {
	const mailbox = "newsletter@example.jp"
	sources := NewSendingSources([]string{"203.0.113.0/24"}, nil, []string{"mail.example.jp"}, nil)
	notice := func(from, subject, subtype string) []byte {
		return multipartMessage("From: "+from+"\r\nTo: newsletter@example.jp\r\nSubject: "+subject+"\r\n", subtype,
			textPart("A new notification is available."), returnedPart("message/rfc822", spoofHop))
	}
	const (
		display = "Mail Delivery System <notify@relay.partner.example.net>"
		plain   = "Notifications <notify@relay.partner.example.net>"
		daemon  = "MAILER-DAEMON@relay.partner.example.net"
		bounce  = "Undeliverable: Invoice"
		neutral = "Message notification"
	)
	for _, c := range []struct {
		name       string
		raw        []byte
		kind, rule string
	}{
		{"display name and bounce subject", notice(display, bounce, "mixed"), bounceKindJunk, ruleBackscatter},
		{"display name only", notice(display, neutral, "mixed"), bounceKindOther, ruleDaemonDisplayName},
		{"bounce subject only", notice(plain, bounce, "mixed"), bounceKindFailed, ruleSubjectPattern},
		{"report without a report-type", notice(daemon, neutral, "report"), bounceKindJunk, ruleBackscatter},
		{"report of the type disposition-notification", notice(daemon, neutral, "report; report-type=disposition-notification"), bounceKindOther, ruleDaemonSender},
	} {
		t.Run(c.name, func(t *testing.T) {
			pm := ParseMessage(c.raw)
			if cls := ClassifyWith(pm, mailbox, sources); cls.Kind != c.kind || cls.Rule != c.rule {
				t.Errorf("classification = %+v, want kind %q by rule %q", cls, c.kind, c.rule)
			}
		})
	}
}

// TestBackscatterSparesSamples: without sending sources configured nothing
// changes for any sample, and with the network the hops of the samples come
// from (192.0.2.0/24) only the forged sample becomes backscatter.
func TestBackscatterSparesSamples(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	sourceNet := NewSendingSources([]string{"192.0.2.0/24"}, nil, []string{"mx1.example.jp"}, nil)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".eml") {
			continue
		}
		pm := ParseMessage(readSample(t, e.Name()))
		for _, mailbox := range []string{"", "newsletter@example.jp"} {
			want := Classify(pm, mailbox)
			if got := ClassifyWith(pm, mailbox, SendingSources{}); got != want {
				t.Errorf("%s (mailbox %q) without sending sources = %+v, want %+v", e.Name(), mailbox, got, want)
			}
			got := ClassifyWith(pm, mailbox, sourceNet)
			if e.Name() == "postfix_backscatter.eml" {
				if got.Rule != ruleBackscatter || got.Kind != bounceKindJunk || got.IsBounce {
					t.Errorf("%s (mailbox %q) = %+v, want backscatter", e.Name(), mailbox, got)
				}
				continue
			}
			if got != want {
				t.Errorf("%s (mailbox %q) with the sending network = %+v, want %+v", e.Name(), mailbox, got, want)
			}
		}
	}
}

// TestNewGroupConfigSendingSources: the classification takes the sending
// sources from WithSendingSources alone; the verification of WithDNS uses
// them too when given, in either order, and the sources given to WithDNS
// otherwise. A nil verifier leaves the sending sources in effect.
func TestNewGroupConfigSendingSources(t *testing.T) {
	sourceA := NewSendingSources(nil, nil, []string{"mx-a.example.jp"}, nil)
	sourceB := NewSendingSources(nil, nil, []string{"mx-b.example.jp"}, nil)
	fake := &fakeVerifier{outcome: DNSOutcomeExists, ok: true}
	cases := []struct {
		name     string
		opts     []GroupOption
		classify string // the host the classification's sources hold ("" = none)
		dns      string // the host the verification's sources hold ("" = no verification)
	}{
		{"sending sources only", []GroupOption{WithSendingSources(sourceB)}, "mx-b.example.jp", ""},
		{"verifier only", []GroupOption{WithDNS(fake, sourceA)}, "", "mx-a.example.jp"},
		{"verifier, then sending sources", []GroupOption{WithDNS(fake, sourceA), WithSendingSources(sourceB)}, "mx-b.example.jp", "mx-b.example.jp"},
		{"sending sources, then verifier", []GroupOption{WithSendingSources(sourceB), WithDNS(fake, sourceA)}, "mx-b.example.jp", "mx-b.example.jp"},
		{"nil verifier", []GroupOption{WithSendingSources(sourceB), WithDNS(nil, sourceA)}, "mx-b.example.jp", ""},
		{"verifier switched off later", []GroupOption{WithDNS(fake, sourceA), WithSendingSources(sourceB), WithDNS(nil, sourceA)}, "mx-b.example.jp", ""},
		{"nil option", []GroupOption{nil, WithSendingSources(sourceB)}, "mx-b.example.jp", ""},
		{"no options", nil, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := newGroupConfig(c.opts)
			if c.classify == "" {
				if !cfg.sources.Empty() {
					t.Errorf("classification sources configured: %+v", cfg.sources)
				}
			} else if !cfg.sources.MatchReceived("from " + c.classify + " (unknown [198.51.100.7]) by mx.partner.example.net") {
				t.Errorf("classification sources do not hold %s", c.classify)
			}
			if c.dns == "" {
				if cfg.dns != nil {
					t.Errorf("verification set up: %+v", cfg.dns)
				}
				return
			}
			if cfg.dns == nil || !cfg.dns.matcher.sources.Match(c.dns) || cfg.dns.verifier != fake {
				t.Errorf("verification = %+v, want the verifier with %s", cfg.dns, c.dns)
			}
		})
	}
}

// TestBackscatterIsJunk files the return notice of forged mail through the
// grouping phase: it is junk (no group, no bounces row), counted apart from
// the target messages, listed under the junk kind and a server deletion
// candidate, whether the DNS lookup failures are verified or not. The
// sources given to WithDNS alone do not make the rule apply, nor do
// addresses without a host name.
func TestBackscatterIsJunk(t *testing.T) {
	sources := NewSendingSources([]string{"192.0.2.0/24"}, nil, []string{"mx1.example.jp"}, nil)
	addresses := NewSendingSources([]string{"192.0.2.0/24"}, nil, nil, nil)
	verifier := func() *fakeVerifier { return &fakeVerifier{outcome: DNSOutcomeExists, ok: true} }
	cases := []struct {
		name string
		opts []GroupOption
		junk bool
	}{
		{"sending sources without the DNS option", []GroupOption{WithSendingSources(sources)}, true},
		{"sending sources with the verification off", []GroupOption{WithSendingSources(sources), WithDNS(nil, sources)}, true},
		{"sending sources with the verification on", []GroupOption{WithDNS(verifier(), SendingSources{}), WithSendingSources(sources)}, true},
		{"the DNS option alone", []GroupOption{WithDNS(verifier(), sources)}, false},
		{"addresses only", []GroupOption{WithSendingSources(addresses)}, false},
		{"no options", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			address := "newsletter@example.jp"
			dir := MailboxDir(root, address)
			db := mustOpenIndex(t, root, address)
			old := time.Now().AddDate(0, 0, -90).UTC()
			for i, name := range []string{"postfix_backscatter.eml", "postfix_dsn.eml"} {
				if _, _, err := storeMessage(db, dir, readSample(t, name), Source{Folder: "INBOX", UIDValidity: 1, UID: uint32(i + 1), ReceivedAt: old},
					storeOptions{writeEML: true}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`UPDATE messages SET date = ?`, old); err != nil {
				t.Fatal(err)
			}
			res, err := GroupMailbox(context.Background(), root, address, false, nil, nil, c.opts...)
			if err != nil {
				t.Fatal(err)
			}
			wantJunk, wantTargets, wantGroups := 0, 2, 2
			if c.junk {
				wantJunk, wantTargets, wantGroups = 1, 1, 1
			}
			if n, _ := CountJunkMessages(db); n != wantJunk {
				t.Errorf("junk messages = %d, want %d", n, wantJunk)
			}
			if n, _ := CountTargetMessages(db); n != wantTargets {
				t.Errorf("target messages = %d, want %d", n, wantTargets)
			}
			if res.Groups != wantGroups || countRows(t, db, "bounces") != wantGroups {
				t.Errorf("%d groups, %d bounces rows; want %d", res.Groups, countRows(t, db, "bounces"), wantGroups)
			}
			counts, err := models.CountMessagesByKind(db, models.MessageFilter{TargetRules: TargetRules()})
			if err != nil || counts != (models.MessageKindCounts{All: 2, Target: wantTargets, Junk: wantJunk}) {
				t.Errorf("kind counts = %+v (err %v)", counts, err)
			}
			junk, _, err := models.ListMessages(db, models.MessageFilter{Kind: models.MessageKindJunk})
			if err != nil || len(junk) != wantJunk {
				t.Fatalf("junk list = %+v (err %v)", junk, err)
			}
			for _, m := range junk {
				if m.Rule != ruleBackscatter || m.IsBounce || m.GroupKey != "" || m.BounceKind != bounceKindJunk {
					t.Errorf("junk %s: rule %q, is_bounce %v, kind %q, group %q", m.MessageKey, m.Rule, m.IsBounce, m.BounceKind, m.GroupKey)
				}
			}
			due, err := models.ListServerDeletionCandidates(db, "INBOX", time.Now().AddDate(0, 0, -30), serverDeletionRules)
			if err != nil || len(due) != 2 {
				t.Errorf("deletion candidates = %+v (err %v), want both notices", due, err)
			}
		})
	}
}

// TestBackscatterFollowsTheSources: a full regroup after the sending sources
// changed moves the notice between bounce and junk (only while a host name
// is configured), and the group it leaves is removed with it.
func TestBackscatterFollowsTheSources(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	msg, _ := storeRaw(t, root, address, readSample(t, "postfix_backscatter.eml"), 1, time.Now().UTC())
	sources := WithSendingSources(NewSendingSources([]string{"192.0.2.0/24"}, nil, []string{"mx1.example.jp"}, nil))
	addresses := WithSendingSources(NewSendingSources([]string{"192.0.2.0/24"}, nil, nil, nil))
	none := WithSendingSources(NewSendingSources(nil, nil, nil, nil))
	steps := []struct {
		name string
		full bool
		opt  GroupOption
		junk bool
	}{
		{"first grouping, nothing configured", false, none, false},
		{"reclassified with the addresses of the sending network only", true, addresses, false},
		{"reclassified with the sending network and MTA", true, sources, true},
		{"reclassified again, configuration removed", true, none, false},
		{"reclassified with the sending network and MTA once more", true, sources, true},
	}
	var groupKey string
	for _, s := range steps {
		res, err := GroupMailbox(context.Background(), root, address, s.full, nil, nil, s.opt)
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		db := mustOpenIndex(t, root, address)
		fresh, err := models.GetMessageByKey(db, msg.MessageKey)
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if s.junk {
			if fresh.Rule != ruleBackscatter || fresh.IsBounce || fresh.GroupKey != "" || res.Groups != 0 || countRows(t, db, "groups") != 0 {
				t.Errorf("%s: message %+v, %d groups; want junk without a group", s.name, fresh, res.Groups)
			}
			continue
		}
		if fresh.Rule != ruleDSNReport || !fresh.IsBounce || fresh.GroupKey == "" || res.Groups != 1 {
			t.Errorf("%s: message %+v, %d groups; want a grouped bounce", s.name, fresh, res.Groups)
		}
		if groupKey != "" && fresh.GroupKey != groupKey {
			t.Errorf("%s: group %s, want the first group %s back", s.name, fresh.GroupKey, groupKey)
		}
		groupKey = fresh.GroupKey
	}
}

// TestSendingSourcesReachTheVerification: with both options the verification
// tells which reporting MTAs are sending servers by the sources of WithSendingSources (the
// reporting MTA resolves to an address that is none of them).
func TestSendingSourcesReachTheVerification(t *testing.T) {
	for _, c := range []struct {
		name     string
		opts     []GroupOption
		category string
		unit     string
	}{
		{"WithDNS sources (empty: every MTA is a sending server)", []GroupOption{WithDNS(&fakeVerifier{outcome: DNSOutcomeExists, ok: true}, SendingSources{})},
			categoryDNSResolverFailure, "mx1.example.jp"},
		{"WithSendingSources overrides them", []GroupOption{
			WithDNS(&resolvingVerifier{fakeVerifier: fakeVerifier{outcome: DNSOutcomeExists, ok: true},
				fakeResolver: fakeResolver{reporters: map[string][]netip.Addr{"mx1.example.jp": addrs("198.51.100.9")}}}, SendingSources{}),
			WithSendingSources(NewSendingSources(nil, nil, []string{"mail.example.jp"}, nil)),
		}, categoryDNSFailureExternal, "infoseek.jp"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			address := "newsletter@example.jp"
			msg, _ := storeRaw(t, root, address, readSample(t, "postfix_delayed_dns.eml"), 1, time.Now().UTC())
			if _, err := GroupMailbox(context.Background(), root, address, false, nil, nil, c.opts...); err != nil {
				t.Fatal(err)
			}
			b, err := models.GetBounceByMessageID(mustOpenIndex(t, root, address), msg.ID)
			if err != nil {
				t.Fatal(err)
			}
			if want := GroupKey(c.category, c.unit, ""); b.GroupKey != want {
				t.Errorf("group_key = %s, want %s (%s: %s)", b.GroupKey, want, c.category, c.unit)
			}
		})
	}
}

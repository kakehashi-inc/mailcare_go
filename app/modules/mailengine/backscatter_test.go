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
// ours, a hop whose only sign of our own network is its address (the name
// is not registered), and a hop whose only sign of our server is its name.
const (
	spoofHop = "Received: from spam-sender.example (unknown [198.51.100.7])\r\n" +
		"\tby mx.partner.example.net (Postfix) with ESMTP id 4ZbCdE1fG2z9tLm\r\n" +
		"\tfor <nobody@partner.example.net>; Wed, 10 Sep 2025 03:12:44 +0900 (JST)\r\n"
	ownHop = "Received: from app02.example.jp (app02.example.jp [203.0.113.5])\r\n" +
		"\tby mx.partner.example.net (Postfix) with ESMTPS id 4ZbCdE1fG2z9tLn\r\n" +
		"\tfor <nobody@partner.example.net>; Wed, 10 Sep 2025 03:12:44 +0900 (JST)\r\n"
	ownNameHop = "Received: from mail.example.jp (unknown [198.51.100.99])\r\n" +
		"\tby mx.partner.example.net (Postfix) with ESMTPS id 4ZbCdE1fG2z9tLp\r\n" +
		"\tfor <nobody@partner.example.net>; Wed, 10 Sep 2025 03:12:44 +0900 (JST)\r\n"
	partnerHop = "Received: from mx.partner.example.net (localhost [127.0.0.1])\r\n" +
		"\tby mailstore.partner.example.net with LMTP id 8Kq2; Wed, 10 Sep 2025 03:12:45 +0900\r\n"
)

// returnedPart is the returned original attached as container
// (message/rfc822 or text/rfc822-headers) with the given headers before
// its own.
func returnedPart(container, headers string) string {
	part := "Content-Type: " + container + "\r\n\r\n" + headers +
		"From: Newsletter <newsletter@example.jp>\r\nTo: nobody@partner.example.net\r\nSubject: Your invoice\r\n" +
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
			if om.Subject != "Your invoice" {
				t.Errorf("subject = %q: the headers after Received were not read", om.Subject)
			}
			if om := ParseMessage(returnedNotice(container, "")).OriginalMessage; om == nil || len(om.Received) != 0 {
				t.Errorf("without Received: %+v", om)
			}
		})
	}
	// The notice's own Received headers are not the original's.
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
	own4 := NewOwnSources([]string{"203.0.113.0/24"}, nil, nil, nil)
	own6 := NewOwnSources(nil, []string{"2001:db8::/32"}, nil, nil)
	hosts := NewOwnSources(nil, nil, []string{"mail.example.jp", "mailhost"}, nil)
	all := NewOwnSources([]string{"203.0.113.0/24"}, []string{"2001:db8::/32"}, []string{"mail.example.jp", "mailhost"}, nil)
	const postfix = "from mail.example.jp (mail.example.jp [203.0.113.5]) by mx.partner.example.net (Postfix) with ESMTPS id 4ZbCdE1fG2z9tLn " +
		"for <taro@partner.example.net>; Wed, 10 Sep 2025 03:12:44 +0900 (JST)"
	cases := []struct {
		name string
		own  OwnSources
		line string
		want bool
	}{
		{"postfix, by address", own4, postfix, true},
		{"postfix, by name", hosts, postfix, true},
		{"postfix, nothing configured", OwnSources{}, postfix, false},
		{"postfix, foreign client", all,
			"from spam-sender.example (unknown [198.51.100.7]) by mx.partner.example.net (Postfix) with ESMTP id 4ZbC; Wed, 10 Sep 2025 03:12:44 +0900", false},
		{"postfix, client address only", own4, "from relay.other.example (unknown [203.0.113.77]) by mx.partner.example.net (Postfix) with ESMTP id 4Z", true},
		{"gmail, IPv6 client with a dotted name", own6,
			"from mail.other.example (mail.other.example. [2001:db8::25]) by mx.google.com with ESMTPS id d9443c01a7336-2a1b " +
				"for <user2@gmail.com> (version=TLS1_3 cipher=TLS_AES_256_GCM_SHA384 bits=256/256); Sun, 07 Sep 2025 00:59:51 -0700 (PDT)", true},
		{"gmail, bare IPv6 after by", own6, "by 2001:db8::1 with SMTP id m5csp1234567; Sun, 7 Sep 2025 00:59:52 -0700 (PDT)", true},
		{"IPv6 tag in parentheses", own6, "from relay.other.example (IPv6:2001:db8::25) by mx.partner.example.net with ESMTP", true},
		{"IPv6 tag in brackets", own6, "from relay.other.example (unknown [IPv6:2001:db8::25]) by mx.partner.example.net with ESMTP", true},
		{"IPv4-mapped address", own4, "from relay.other.example ([::ffff:203.0.113.5]) by mx.partner.example.net with LMTP id x", true},
		{"exchange, bare address in parentheses", own4,
			"from EXCH01.corp.other.example (203.0.113.30) by mx.partner.example.net with Microsoft SMTP Server id 15.20.8205.10; Tue, 9 Sep 2025 10:00:00 +0900", true},
		{"exchange, bare IPv6 in parentheses", own6,
			"from JPXP123MB4567.JPNP123.PROD.OUTLOOK.COM (2001:db8:400:1::10) by JPXP123MB9999.JPNP123.PROD.OUTLOOK.COM (2603:1096:400::20) with Microsoft SMTP Server", true},
		{"address with a port in parentheses", own4, "from unknown (HELO relay) (203.0.113.5:41234) by mx.partner.example.net with SMTP", true},
		{"bare address after from", own4, "from 203.0.113.5 by mx.partner.example.net with SMTP", true},
		{"host name, case and trailing dot", hosts, "from MAIL.Example.JP. (unknown [198.51.100.7]) by mx.partner.example.net with ESMTP", true},
		{"host name after by", hosts, "from app01.other.example (app01.other.example [198.51.100.20]) by mail.example.jp (Postfix) with ESMTP id 4Xk1", true},
		{"host name looked up by the receiver", hosts, "from localhost.localdomain (mail.example.jp [198.51.100.7]) by mx.partner.example.net with ESMTP", true},
		{"exim helo name", hosts, "from [198.51.100.7] (port=41234 helo=mail.example.jp) by mail.partner.example.net with esmtpsa (TLS1.3) (Exim 4.96)", true},
		{"qmail HELO name, single label", hosts, "from unknown (HELO mailhost) (198.51.100.7) by mx.partner.example.net with SMTP; 5 Sep 2025 02:29:58 -0000", true},
		{"host names only, the address is not looked up", hosts, "from relay.other.example (relay.other.example [203.0.113.5]) by mx.partner.example.net", false},
		{"look-alike host names", hosts, "from mail.example.jp.evil.example (notmail.example.jp [198.51.100.7]) by mx.mail-example.jp", false},
		{"IPv4 configured, IPv6 seen", own4, "from relay.other.example (unknown [2001:db8::25]) by mx.partner.example.net", false},
		{"IPv6 configured, IPv4 seen", own6, "from relay.other.example (unknown [203.0.113.5]) by mx.partner.example.net", false},
		{"address outside the hop positions", own4, "from spam-sender.example (unknown [198.51.100.7]) by mx.partner.example.net with ESMTP id 203.0.113.5", false},
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
			if got := c.own.MatchReceived(c.line); got != c.want {
				addrs, hosts, ok := receivedSources(c.line)
				t.Errorf("MatchReceived(%q) = %v, want %v (addresses %v, names %q, judged %v)", c.line, got, c.want, addrs, hosts, ok)
			}
		})
	}
}

// bracketedHops returns a Received header from a host none of ours with n
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

// TestReceivedBounds: the Received headers of an original are judged only
// within the bounds (at most 100 headers, 8 KB per header, 64 distinct
// addresses per header, repeats counted once); past one the original is not
// judged, MatchReceived counts the line as showing ours, and the backscatter
// rule leaves the notice alone. Up to the bounds the rule applies as before.
func TestReceivedBounds(t *testing.T) {
	own := NewOwnSources([]string{"203.0.113.0/24"}, nil, []string{"mail.example.jp"}, nil)
	const hop = "from spam-sender.example (unknown [198.51.100.7]) by mx.partner.example.net (Postfix) with ESMTP"
	pad := func(size int) string {
		return hop + " id " + strings.Repeat("x", size-len(hop)-len(" id "))
	}
	for _, c := range []struct {
		name   string
		lines  []string
		judged bool
	}{
		{"64 distinct addresses", []string{bracketedHops(64, 1)}, true},
		{"65 distinct addresses", []string{bracketedHops(65, 1)}, false},
		{"64 distinct addresses, each three times", []string{bracketedHops(64, 3)}, true},
		{"8 KB", []string{pad(maxReceivedLineBytes)}, true},
		{"8 KB and one byte", []string{pad(maxReceivedLineBytes + 1)}, false},
		{"100 headers", slices.Repeat([]string{hop}, 100), true},
		{"101 headers", slices.Repeat([]string{hop}, 101), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if ours, judged := own.matchReceivedLines(c.lines); ours || judged != c.judged {
				t.Errorf("matchReceivedLines = %v, %v; want not ours, judged %v", ours, judged, c.judged)
			}
			if got := own.MatchReceived(c.lines[0]); len(c.lines) == 1 && got != !c.judged {
				t.Errorf("MatchReceived = %v, want %v", got, !c.judged)
			}
			var headers strings.Builder
			for _, l := range c.lines {
				headers.WriteString("Received: " + l + "\r\n")
			}
			cls := ClassifyWith(ParseMessage(returnedNotice("message/rfc822", headers.String())), "newsletter@example.jp", own)
			want := ruleDSNReport
			if c.judged {
				want = ruleBackscatter
			}
			if cls.Rule != want {
				t.Errorf("rule %q, want %q", cls.Rule, want)
			}
		})
	}
	// An address of ours past the bounds is not looked for: the line is not
	// judged either way.
	if own.MatchReceived(bracketedHops(70, 1)+" [203.0.113.5]") != true {
		t.Error("an unjudged line does not count as showing ours")
	}
}

// hugeHop returns a Received header of at least size bytes of bracketed
// addresses, none of ours.
func hugeHop(size int) string {
	var b strings.Builder
	b.WriteString("from spam-sender.example (unknown")
	for b.Len() < size {
		fmt.Fprintf(&b, " [198.51.%d.%d]", b.Len()%250, b.Len()%251)
	}
	b.WriteString(") by mx.partner.example.net")
	return b.String()
}

// TestReceivedHostileOriginalIsCheap: judged against 1000 prefixes (an SPF
// record at its bound), a single Received header of 8 MB of bracketed
// addresses costs next to nothing and is not judged; so is an original
// with a header of almost 1 MB (the most the parser reads) or with
// thousands of short headers, which the backscatter rule leaves alone.
func TestReceivedHostileOriginalIsCheap(t *testing.T) {
	var prefixes []netip.Prefix
	for i := range 1000 {
		prefixes = append(prefixes, netip.PrefixFrom(netip.AddrFrom4([4]byte{10, byte(i / 250), byte(i % 250), 0}), 24))
	}
	own := NewOwnSources(nil, nil, []string{"mail.example.jp"}, nil).withResolved(nil, prefixes, nil)
	line := hugeHop(8 << 20)
	start := time.Now()
	ours, judged := own.matchReceivedLines([]string{line})
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("an 8 MB header judged in %v", elapsed)
	}
	if ours || judged || !own.MatchReceived(line) {
		t.Errorf("an 8 MB header: ours %v, judged %v; want not judged (counted as ours by MatchReceived)", ours, judged)
	}
	big := "Received: " + hugeHop(1<<20-4096) + "\r\n"
	many := strings.Repeat("Received: from spam-sender.example (unknown [198.51.100.7]) by mx.partner.example.net\r\n", 5000)
	for name, headers := range map[string]string{"1 MB header": big, "5000 headers": many} {
		pm := ParseMessage(returnedNotice("message/rfc822", headers))
		if pm.OriginalMessage == nil || len(pm.OriginalMessage.Received) == 0 {
			t.Fatalf("%s: the original was not read", name)
		}
		start := time.Now()
		cls := ClassifyWith(pm, "newsletter@example.jp", own)
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Errorf("%s: classified in %v", name, elapsed)
		}
		if cls.Rule != ruleDSNReport {
			t.Errorf("%s: rule %q, want %q (not judged)", name, cls.Rule, ruleDSNReport)
		}
	}
}

// TestMatchReceivedNothingConfigured: without an entry in any of the three
// settings nothing matches, not even an address handed to NewOwnSources as
// already resolved (resolved addresses alone are no configuration), nor the
// host name of a blank entry.
func TestMatchReceivedNothingConfigured(t *testing.T) {
	const line = "from mail.example.jp (mail.example.jp [203.0.113.5]) by mx.partner.example.net (Postfix) with ESMTPS id 4Zb"
	own := NewOwnSources([]string{" "}, []string{""}, []string{" "}, addrs("203.0.113.5"))
	if !own.Empty() {
		t.Fatal("blank entries and resolved addresses count as configured")
	}
	if own.MatchReceived(line) {
		t.Errorf("MatchReceived(%q) = true with nothing configured", line)
	}
	// The same resolved address matches once an entry is configured.
	if !NewOwnSources(nil, nil, []string{"other.example.jp"}, addrs("203.0.113.5")).MatchReceived(line) {
		t.Error("the resolved address does not match with a host name configured")
	}
}

// TestClassifyBackscatter covers the backscatter rule (design document "mail
// classification" 2.6): the return notice of a message none of our own
// sending servers handed on is junk, and the notices about our own mail
// (shown in the Received headers or reported by our own MTA), the notices
// without a returned original or its Received headers, the other reports
// (DMARC, read receipts), the automatic replies, the forwards of a person
// and every message without one of our host names configured keep their
// classification.
func TestClassifyBackscatter(t *testing.T) {
	const mailbox = "newsletter@example.jp"
	ownNet := NewOwnSources([]string{"203.0.113.0/24"}, nil, []string{"mail.example.jp"}, nil)
	ownAll := NewOwnSources([]string{"203.0.113.0/24"}, []string{"2001:db8::/32"}, []string{"mail.example.jp"}, nil)
	ownHosts := NewOwnSources(nil, nil, []string{"mail.example.jp"}, nil)
	ownAddrs := NewOwnSources([]string{"203.0.113.0/24"}, []string{"2001:db8::/32"}, nil, nil)
	ownMTA := NewOwnSources(nil, nil, []string{"mx1.example.jp"}, nil)
	elsewhere := NewOwnSources([]string{"192.0.2.250"}, nil, []string{"elsewhere.example.jp"}, nil)
	rfc822 := func(headers string) []byte { return returnedNotice("message/rfc822", headers) }
	// The delayed warning of our own MTA (mx1.example.jp), returning the
	// headers of our mail with the hop from the application server to the
	// MTA, and the same warning about mail whose hops name none of our hosts
	// (submitted through a server that is not registered).
	delayedWith := func(received string) []byte {
		return bytes.Replace(readSample(t, "postfix_delayed_dns.eml"), []byte("From: Newsletter <newsletter@example.jp>\n"),
			[]byte(received+"From: Newsletter <newsletter@example.jp>\n"), 1)
	}
	ownDelayed := delayedWith("Received: from app01.example.jp (app01.example.jp [192.0.2.20])\n\tby mx1.example.jp (Postfix) with ESMTP id 4Xk1Yy6Q8Fz9tLn\n" +
		"\tfor <user1@infoseek.jp>; Wed, 3 Sep 2025 08:59:01 +0900 (JST)\n")
	ownDelayedForeignHops := delayedWith("Received: from laptop.home.example (unknown [198.51.100.7])\n" +
		"\tby submit.other.example (Postfix) with ESMTPSA id 4Xk1Yy6Q8Fz9tLm; Wed, 3 Sep 2025 08:59:01 +0900 (JST)\n")
	// A notice whose reporting MTA is an address of our network.
	reportedByAddress := bytes.Replace(dnsNotice(bounceKindFailed, "5.4.4", "n@nowhere.example", "[203.0.113.25]",
		"Name service error for name=nowhere.example type=MX: Host not found"),
		[]byte("From: Newsletter <newsletter@example.jp>\r\n"), []byte(spoofHop+"From: Newsletter <newsletter@example.jp>\r\n"), 1)
	notice := "From: postmaster@mx.partner.example.net\r\nTo: newsletter@example.jp\r\nSubject: Undeliverable: Your invoice\r\n"
	wording := "Delivery has failed to these recipients or groups:\r\nnobody@partner.example.net"
	relay := "From: Notifications <notify@relay.partner.example.net>\r\nTo: newsletter@example.jp\r\nSubject: Message notification\r\n"
	person := "From: Hanako <hanako@partner.example.com>\r\nTo: newsletter@example.jp\r\n"
	oof := person + "Auto-Submitted: auto-replied\r\n"
	report := gzipped(t, dmarcXML(recSPFMissing))
	cases := []struct {
		name    string
		raw     []byte
		own     OwnSources
		mailbox string // "" = mailbox; "-" = unknown
		kind    string
		rule    string
		// foreign, when set, is a configuration under which the same message
		// is backscatter: the case reaches the rule and is spared only by
		// what it tests.
		foreign OwnSources
		// without is the rule of a backscatter case without our own sources
		// ("" = dsn_report, "-" = ordinary mail).
		without string
	}{
		// The return notices of forged mail, reported by the receiver's MTA.
		{name: "spoofed postfix sample", raw: readSample(t, "postfix_backscatter.eml"), own: ownNet, kind: bounceKindJunk, rule: ruleBackscatter},
		{name: "spoofed, original attached whole", raw: rfc822(spoofHop), own: ownNet, kind: bounceKindJunk, rule: ruleBackscatter},
		{name: "spoofed, original headers only", raw: returnedNotice("text/rfc822-headers", spoofHop), own: ownNet, kind: bounceKindJunk, rule: ruleBackscatter},
		{name: "spoofed, several hops", raw: rfc822(partnerHop + spoofHop), own: ownAll, kind: bounceKindJunk, rule: ruleBackscatter},
		{name: "spoofed, host names configured", raw: rfc822(spoofHop), own: ownHosts, kind: bounceKindJunk, rule: ruleBackscatter},
		{name: "spoofed, our MTA configured", raw: rfc822(spoofHop), own: ownMTA, kind: bounceKindJunk, rule: ruleBackscatter},
		{name: "spoofed, signed by another domain", raw: rfc822("DKIM-Signature: v=1; a=rsa-sha256; d=spam-sender.example; s=s1; bh=x; b=y\r\n" + spoofHop),
			own: ownNet, kind: bounceKindJunk, rule: ruleBackscatter},
		{name: "spoofed, signed with our domain, monitored domain unknown",
			raw: rfc822("DKIM-Signature: v=1; a=rsa-sha256; d=example.jp; s=s1; bh=x; b=y\r\n" + spoofHop),
			own: ownNet, mailbox: "-", kind: bounceKindJunk, rule: ruleBackscatter},
		// Only an address is shown for a host registered by its name: the
		// name is not looked up (a documented limit: register the addresses
		// too).
		{name: "host names configured, hop shown by its address only",
			raw: rfc822("Received: from relay.other.example (relay.other.example [203.0.113.5]) by mx.partner.example.net with ESMTP\r\n"),
			own: ownHosts, kind: bounceKindJunk, rule: ruleBackscatter},
		{name: "notice without delivery-status, by the subject and the sender", own: ownNet, kind: bounceKindJunk, rule: ruleBackscatter, without: ruleDaemonSender,
			raw: multipartMessage(notice, "mixed", textPart(wording), returnedPart("message/rfc822", spoofHop))},
		{name: "notice without delivery-status, by the HTML wording only", own: ownNet, kind: bounceKindJunk, rule: ruleBackscatter, without: ruleBodyPattern,
			raw: multipartMessage(relay, "mixed", textPart("You have a new notification."), htmlPart("<p>"+wording+"</p>"),
				returnedPart("message/rfc822", spoofHop))},
		// A forward prefix does not spare a daemon's mail.
		{name: "forward prefix from a daemon address", own: ownNet, kind: bounceKindJunk, rule: ruleBackscatter, without: ruleDaemonSender,
			raw: multipartMessage("From: MAILER-DAEMON@relay.partner.example.net\r\nTo: newsletter@example.jp\r\nSubject: Fwd: Undeliverable: Your invoice\r\n",
				"mixed", textPart(wording), returnedPart("message/rfc822", spoofHop))},

		// Without one of our host names nothing is judged: our own MTA names
		// itself in the hops of our mail, the addresses there are the peers'.
		{name: "addresses only configured", raw: rfc822(spoofHop), own: ownAddrs, kind: bounceKindFailed, rule: ruleDSNReport, foreign: ownNet},
		{name: "delayed warning of our own MTA, only the MTA's address configured", raw: ownDelayed,
			own: NewOwnSources([]string{"192.0.2.10"}, nil, nil, nil), kind: bounceKindDelayed, rule: ruleDSNReport, foreign: elsewhere},

		// The notices about our own mail.
		{name: "own hop by address", raw: rfc822(ownHop), own: ownNet, kind: bounceKindFailed, rule: ruleDSNReport, foreign: elsewhere},
		{name: "own hop by name", raw: rfc822(ownNameHop), own: ownAll, kind: bounceKindFailed, rule: ruleDSNReport, foreign: elsewhere},
		{name: "own hop by name, host names configured", raw: rfc822(ownNameHop), own: ownHosts, kind: bounceKindFailed, rule: ruleDSNReport, foreign: elsewhere},
		{name: "own hop below the receiver's hops", raw: rfc822(partnerHop + ownHop), own: ownNet, kind: bounceKindFailed, rule: ruleDSNReport, foreign: elsewhere},
		{name: "own hop by IPv6", own: ownAll, kind: bounceKindFailed, rule: ruleDSNReport, foreign: elsewhere,
			raw: rfc822("Received: from mail6.other.example (mail6.other.example [IPv6:2001:db8::25]) by mx.partner.example.net with ESMTPS\r\n")},
		{name: "signed with our domain", raw: rfc822("DKIM-Signature: v=1; a=rsa-sha256; d=example.jp; s=s1; bh=x; b=y\r\n" + spoofHop),
			own: ownNet, kind: bounceKindFailed, rule: ruleDSNReport},
		{name: "signed with a subdomain of ours", raw: rfc822("DKIM-Signature: v=1; a=rsa-sha256; d=Mail.Example.JP; s=s1; bh=x; b=y\r\n" + spoofHop),
			own: ownNet, kind: bounceKindFailed, rule: ruleDSNReport},
		{name: "delayed warning of our own MTA", raw: ownDelayed, own: ownMTA, kind: bounceKindDelayed, rule: ruleDSNReport, foreign: elsewhere},
		// Our own MTA reports about mail it handled, whatever the hops say.
		{name: "delayed warning of our own MTA, hops naming none of ours", raw: ownDelayedForeignHops,
			own: ownMTA, kind: bounceKindDelayed, rule: ruleDSNReport, foreign: elsewhere},
		{name: "reported by an address of our network", raw: reportedByAddress, own: ownNet, kind: bounceKindFailed, rule: ruleDSNReport, foreign: elsewhere},
		{name: "delayed warning sample without Received", raw: readSample(t, "postfix_delayed_dns.eml"),
			own: ownMTA, kind: bounceKindDelayed, rule: ruleDSNReport},

		// Nothing to judge.
		{name: "nothing configured", raw: readSample(t, "postfix_backscatter.eml"), kind: bounceKindFailed, rule: ruleDSNReport, foreign: ownNet},
		{name: "original without Received", raw: rfc822(""), own: ownNet, kind: bounceKindFailed, rule: ruleDSNReport},
		{name: "original headers without Received", raw: returnedNotice("text/rfc822-headers", ""), own: ownNet, kind: bounceKindFailed, rule: ruleDSNReport},
		{name: "no original attached", raw: dsnMessage("Undelivered Mail Returned to Sender", "Action: failed\r\nStatus: 5.1.1\r\n"),
			own: ownNet, kind: bounceKindFailed, rule: ruleDSNReport},
		{name: "DMARC aggregate report", raw: dmarcMail("application/gzip", "r.xml.gz", report), mailbox: "dmarc@example.jp",
			own: ownNet, kind: bounceKindReport, rule: ruleDMARCReport},
		// A read receipt is a report of another type, never a return notice.
		{name: "read receipt of forged mail", own: ownNet, kind: "", rule: "",
			raw: multipartMessage("From: Taro <nobody@partner.example.net>\r\nTo: newsletter@example.jp\r\nSubject: Read: Your invoice\r\n",
				"report; report-type=disposition-notification", textPart("Your message was displayed."),
				"Content-Type: message/disposition-notification\r\n\r\nReporting-UA: partner.example.net; Mailer\r\n"+
					"Final-Recipient: rfc822; nobody@partner.example.net\r\nDisposition: manual-action/MDN-sent-manually; displayed\r\n",
				returnedPart("text/rfc822-headers", spoofHop))},
		{name: "read receipt that looks like a notice", own: ownNet, kind: bounceKindFailed, rule: ruleDaemonSender,
			raw: multipartMessage("From: postmaster@partner.example.net\r\nTo: newsletter@example.jp\r\nSubject: Read: Undeliverable: Your invoice\r\n",
				"report; report-type=disposition-notification", textPart("Your message was displayed."),
				"Content-Type: message/disposition-notification\r\n\r\nReporting-UA: partner.example.net; Mailer\r\n"+
					"Final-Recipient: rfc822; nobody@partner.example.net\r\nDisposition: automatic-action/MDN-sent-automatically; displayed\r\n",
				returnedPart("text/rfc822-headers", spoofHop))},
		{name: "automatic reply sample", raw: readSample(t, "autoreply.eml"), own: ownNet, kind: bounceKindAutoReply, rule: ruleAutoReply},
		{name: "out-of-office reply with the original attached", own: ownNet, kind: bounceKindAutoReply, rule: ruleAutoReply,
			raw: multipartMessage(oof+"Subject: Automatic reply: Your invoice\r\n", "mixed",
				textPart("I am out of the office until Monday."), returnedPart("message/rfc822", spoofHop))},
		// An automatic reply quoting a bounce subject looks like a notice by
		// its subject, but the out-of-office subject spares it.
		{name: "automatic reply quoting a bounce subject, forged original", own: ownNet, kind: bounceKindAutoReply, rule: ruleAutoReply,
			raw: multipartMessage(oof+"Subject: Automatic reply: Undeliverable: Your invoice\r\n", "mixed",
				textPart("I am out of the office."), returnedPart("message/rfc822", spoofHop))},
		{name: "out-of-office subject quoting a bounce subject, no Auto-Submitted", own: ownNet, kind: bounceKindAutoReply, rule: ruleAutoReplySubject,
			raw: multipartMessage(person+"Subject: Out of Office: Undeliverable: Your invoice\r\n", "mixed",
				textPart("I am out of the office."), returnedPart("message/rfc822", spoofHop))},
		// A person forwarding a notice of forged mail with the notice attached.
		{name: "forward of a person", own: ownNet, kind: bounceKindFailed, rule: ruleSubjectPattern,
			raw: multipartMessage(person+"Subject: Fwd: Undeliverable: Your invoice\r\n", "mixed",
				textPart("Is this ours?"), returnedPart("message/rfc822", spoofHop))},
		{name: "a forwarded message is no notice", own: ownNet, kind: "", rule: "",
			raw: multipartMessage("From: Camille <camille@partner.example.fr>\r\nTo: newsletter@example.jp\r\nSubject: Fwd: Your invoice\r\n", "mixed",
				textPart("Is this yours?"), returnedPart("message/rfc822", spoofHop))},
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
			cls := ClassifyWith(pm, mb, c.own)
			if cls.Kind != c.kind || cls.Rule != c.rule || cls.IsBounce != (c.kind != bounceKindJunk && c.kind != "") {
				t.Fatalf("classification = %+v, want kind %q rule %q", cls, c.kind, c.rule)
			}
			plain := Classify(pm, mb)
			if plain.Rule == ruleBackscatter {
				t.Error("Classify without own sources judged backscatter")
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
				t.Errorf("without own sources = %+v, want rule %q", plain, c.without)
			}
			// The rule reads no body of its own: both passes give the same
			// answer, and the first decides.
			id := newMailboxIdentity(mb)
			id.own = c.own
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
	if cls := ClassifyWith(pm, "dmarc@example.jp", ownNet); cls.Rule != ruleDMARCReport {
		t.Errorf("DMARC report with a forged original = %+v, want the report", cls)
	}
	pm.DMARC = nil
	if id := newMailboxIdentity("dmarc@example.jp"); !isBackscatter(pm, mailboxIdentity{domain: id.domain, own: ownNet}) {
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
	own := NewOwnSources([]string{"203.0.113.0/24"}, nil, []string{"mail.example.jp"}, nil)
	notice := func(from, subject, subtype string) []byte {
		return multipartMessage("From: "+from+"\r\nTo: newsletter@example.jp\r\nSubject: "+subject+"\r\n", subtype,
			textPart("You have a new notification."), returnedPart("message/rfc822", spoofHop))
	}
	const (
		display = "Mail Delivery System <notify@relay.partner.example.net>"
		plain   = "Notifications <notify@relay.partner.example.net>"
		daemon  = "MAILER-DAEMON@relay.partner.example.net"
		bounce  = "Undeliverable: Your invoice"
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
			if cls := ClassifyWith(pm, mailbox, own); cls.Kind != c.kind || cls.Rule != c.rule {
				t.Errorf("classification = %+v, want kind %q by rule %q", cls, c.kind, c.rule)
			}
		})
	}
}

// TestBackscatterSparesSamples: without our own sources configured nothing
// changes for any sample, and with the network the samples' own hops come
// from (192.0.2.0/24) only the forged sample becomes backscatter.
func TestBackscatterSparesSamples(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	ownNet := NewOwnSources([]string{"192.0.2.0/24"}, nil, []string{"mx1.example.jp"}, nil)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".eml") {
			continue
		}
		pm := ParseMessage(readSample(t, e.Name()))
		for _, mailbox := range []string{"", "newsletter@example.jp"} {
			want := Classify(pm, mailbox)
			if got := ClassifyWith(pm, mailbox, OwnSources{}); got != want {
				t.Errorf("%s (mailbox %q) without own sources = %+v, want %+v", e.Name(), mailbox, got, want)
			}
			got := ClassifyWith(pm, mailbox, ownNet)
			if e.Name() == "postfix_backscatter.eml" {
				if got.Rule != ruleBackscatter || got.Kind != bounceKindJunk || got.IsBounce {
					t.Errorf("%s (mailbox %q) = %+v, want backscatter", e.Name(), mailbox, got)
				}
				continue
			}
			if got != want {
				t.Errorf("%s (mailbox %q) with our own network = %+v, want %+v", e.Name(), mailbox, got, want)
			}
		}
	}
}

// TestNewGroupConfigOwnSources: the classification takes our own sources
// from WithOwnSources alone; the verification of WithDNS uses them too
// when given, in either order, and its own sources otherwise. A nil
// verifier leaves the own sources in effect.
func TestNewGroupConfigOwnSources(t *testing.T) {
	ownA := NewOwnSources(nil, nil, []string{"mx-a.example.jp"}, nil)
	ownB := NewOwnSources(nil, nil, []string{"mx-b.example.jp"}, nil)
	fake := &fakeVerifier{outcome: DNSOutcomeExists, ok: true}
	cases := []struct {
		name     string
		opts     []GroupOption
		classify string // the host the classification's sources hold ("" = none)
		dns      string // the host the verification's sources hold ("" = no verification)
	}{
		{"own sources only", []GroupOption{WithOwnSources(ownB)}, "mx-b.example.jp", ""},
		{"verifier only", []GroupOption{WithDNS(fake, ownA)}, "", "mx-a.example.jp"},
		{"verifier, then own sources", []GroupOption{WithDNS(fake, ownA), WithOwnSources(ownB)}, "mx-b.example.jp", "mx-b.example.jp"},
		{"own sources, then verifier", []GroupOption{WithOwnSources(ownB), WithDNS(fake, ownA)}, "mx-b.example.jp", "mx-b.example.jp"},
		{"nil verifier", []GroupOption{WithOwnSources(ownB), WithDNS(nil, ownA)}, "mx-b.example.jp", ""},
		{"verifier switched off later", []GroupOption{WithDNS(fake, ownA), WithOwnSources(ownB), WithDNS(nil, ownA)}, "mx-b.example.jp", ""},
		{"nil option", []GroupOption{nil, WithOwnSources(ownB)}, "mx-b.example.jp", ""},
		{"no options", nil, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := newGroupConfig(c.opts)
			if c.classify == "" {
				if !cfg.own.Empty() {
					t.Errorf("classification sources configured: %+v", cfg.own)
				}
			} else if !cfg.own.MatchReceived("from " + c.classify + " (unknown [198.51.100.7]) by mx.partner.example.net") {
				t.Errorf("classification sources do not hold %s", c.classify)
			}
			if c.dns == "" {
				if cfg.dns != nil {
					t.Errorf("verification set up: %+v", cfg.dns)
				}
				return
			}
			if cfg.dns == nil || !cfg.dns.own.own.Match(c.dns) || cfg.dns.verifier != fake {
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
	own := NewOwnSources([]string{"192.0.2.0/24"}, nil, []string{"mx1.example.jp"}, nil)
	addresses := NewOwnSources([]string{"192.0.2.0/24"}, nil, nil, nil)
	verifier := func() *fakeVerifier { return &fakeVerifier{outcome: DNSOutcomeExists, ok: true} }
	cases := []struct {
		name string
		opts []GroupOption
		junk bool
	}{
		{"own sources without the DNS option", []GroupOption{WithOwnSources(own)}, true},
		{"own sources with the verification off", []GroupOption{WithOwnSources(own), WithDNS(nil, own)}, true},
		{"own sources with the verification on", []GroupOption{WithDNS(verifier(), OwnSources{}), WithOwnSources(own)}, true},
		{"the DNS option alone", []GroupOption{WithDNS(verifier(), own)}, false},
		{"addresses only", []GroupOption{WithOwnSources(addresses)}, false},
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

// TestBackscatterFollowsTheSources: a full regroup after the own sources
// changed moves the notice between bounce and junk (only while a host name
// is configured), and the group it leaves is removed with it.
func TestBackscatterFollowsTheSources(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	msg, _ := storeRaw(t, root, address, readSample(t, "postfix_backscatter.eml"), 1, time.Now().UTC())
	own := WithOwnSources(NewOwnSources([]string{"192.0.2.0/24"}, nil, []string{"mx1.example.jp"}, nil))
	addresses := WithOwnSources(NewOwnSources([]string{"192.0.2.0/24"}, nil, nil, nil))
	none := WithOwnSources(NewOwnSources(nil, nil, nil, nil))
	steps := []struct {
		name string
		full bool
		opt  GroupOption
		junk bool
	}{
		{"first grouping, nothing configured", false, none, false},
		{"reclassified with our network's addresses only", true, addresses, false},
		{"reclassified with our network and MTA", true, own, true},
		{"reclassified again, configuration removed", true, none, false},
		{"reclassified with our network and MTA once more", true, own, true},
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

// TestOwnSourcesReachTheVerification: with both options the verification
// tells our own reporting MTAs by the sources of WithOwnSources (the
// reporting MTA resolves to an address that is none of them).
func TestOwnSourcesReachTheVerification(t *testing.T) {
	for _, c := range []struct {
		name     string
		opts     []GroupOption
		category string
		unit     string
	}{
		{"WithDNS sources (empty: every MTA is ours)", []GroupOption{WithDNS(&fakeVerifier{outcome: DNSOutcomeExists, ok: true}, OwnSources{})},
			categoryDNSResolverFailure, "mx1.example.jp"},
		{"WithOwnSources overrides them", []GroupOption{
			WithDNS(&resolvingVerifier{fakeVerifier: fakeVerifier{outcome: DNSOutcomeExists, ok: true},
				fakeResolver: fakeResolver{reporters: map[string][]netip.Addr{"mx1.example.jp": addrs("198.51.100.9")}}}, OwnSources{}),
			WithOwnSources(NewOwnSources(nil, nil, []string{"mail.example.jp"}, nil)),
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

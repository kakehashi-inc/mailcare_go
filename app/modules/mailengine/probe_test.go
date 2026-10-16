package mailengine

import (
	"encoding/base64"
	"testing"
)

// A message is a probe mail only when its probe header holds the token alone
// and its subject carries "[<identifier>:<token>]": the raw Subject field, its
// RFC 2047 decoding (a relay that puts a non-ASCII tag in front may re-encode
// it) or the subject of the envelope.
func TestProbeMessageToken(t *testing.T) {
	tok := "0123456789abcdef"
	h := "X-MailCare-Probe"
	enc := "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte("【迷惑】[MC:"+tok+"] Probe")) + "?="
	cases := []struct {
		name, env, section string
		wantTag, wantTok   string
	}{
		{"plain", "", "Subject: [MC:" + tok + "] Probe\r\nX-MailCare-Probe: " + tok + "\r\n\r\n", "MC", tok},
		{"tag in front", "", "Subject: [SPAM] [OPS-1:" + tok + "] Probe\r\nX-MailCare-Probe: " + tok + "\r\n\r\n", "OPS-1", tok},
		{"encoded subject", "", "Subject: " + enc + "\r\nX-MailCare-Probe: " + tok + "\r\n\r\n", "MC", tok},
		{"encoded raw, decoded envelope", "【迷惑】[MC:" + tok + "] Probe", "Subject: " + enc + "\r\nX-MailCare-Probe: " + tok + "\r\n\r\n", "MC", tok},
		{"folded header value", "", "Subject: [MC:" + tok + "]\r\n Probe\r\nX-MailCare-Probe:\r\n " + tok + "\r\n\r\n", "MC", tok},
		{"lower-case names", "", "subject: [MC:" + tok + "] Probe\r\nx-mailcare-probe: " + tok + "\r\n\r\n", "MC", tok},
		{"two probe headers", "", "Subject: [MC:" + tok + "] Probe\r\nX-MailCare-Probe: " + tok + "\r\nX-MailCare-Probe: " + tok + "\r\n\r\n", "", ""},
		{"upper-case hex", "", "Subject: [MC:0123456789ABCDEF] Probe\r\nX-MailCare-Probe: 0123456789ABCDEF\r\n\r\n", "", ""},
		{"encoded probe header", "", "Subject: [MC:" + tok + "] Probe\r\nX-MailCare-Probe: =?utf-8?q?" + tok + "?=\r\n\r\n", "", ""},
		{"reply without header", "", "Subject: Re: [MC:" + tok + "] Probe\r\n\r\n", "", ""},
		{"other number in the subject", "", "Subject: [MC:fedcba9876543210] Probe\r\nX-MailCare-Probe: " + tok + "\r\n\r\n", "", ""},
		{"two subjects, envelope", "[MC:" + tok + "] Probe", "Subject: a\r\nSubject: b\r\nX-MailCare-Probe: " + tok + "\r\n\r\n", "MC", tok},
		{"bare LF", "", "Subject: [MC:" + tok + "] Probe\nX-MailCare-Probe: " + tok + "\n\n", "MC", tok},
		{"space before colon", "", "Subject: [MC:" + tok + "] Probe\r\nX-MailCare-Probe : " + tok + "\r\n\r\n", "MC", tok},
	}
	for _, c := range cases {
		tag, got := probeMessageToken(c.env, []byte(c.section), h)
		if tag != c.wantTag || got != c.wantTok {
			t.Errorf("%s: got %q %q, want %q %q", c.name, tag, got, c.wantTag, c.wantTok)
		}
	}
}

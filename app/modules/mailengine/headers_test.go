package mailengine

import (
	"fmt"
	"strings"
	"testing"
)

func TestHeaderText(t *testing.T) {
	raw := "Received: from a.example\r\n by b.example; Mon, 1 Sep 2025 00:00:00 +0000\r\n" +
		"Subject: =?UTF-8?B?44OG44K544OI?=\r\n" +
		"Received: from c.example\r\n" +
		"\r\n" +
		"Body: not a header\r\n"
	got := HeaderText([]byte(raw))
	want := "Received: from a.example by b.example; Mon, 1 Sep 2025 00:00:00 +0000\n" +
		"Subject: テスト\n" +
		"Received: from c.example\n"
	if got != want {
		t.Errorf("HeaderText = %q, want %q", got, want)
	}
	// A block the reader refuses is returned as it is.
	bad := "no colon here\r\nSubject: x\r\n\r\nbody"
	if got := HeaderText([]byte(bad)); !strings.HasPrefix(got, "no colon here\nSubject: x") {
		t.Errorf("fallback = %q", got)
	}
}

// A header block longer than the default bound of go-message (1 MB) is read
// like any other: the message is in memory whole anyway.
func TestParseMessageLongHeader(t *testing.T) {
	var b strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&b, "Received: from relay%d.example.net ([192.0.2.1]) by mx.example.jp with ESMTP id %s; Tue, 1 Sep 2026 12:00:00 +0900\r\n",
			i, strings.Repeat("x", 300))
	}
	raw := b.String() + "From: a@example.net\r\nTo: b@example.jp\r\nSubject: long header\r\nContent-Type: text/plain\r\n\r\nbody\r\n"
	if len(raw) <= 1<<20 {
		t.Fatalf("fixture of %d bytes is not longer than 1 MB", len(raw))
	}
	pm := ParseMessage([]byte(raw))
	if pm.ParseError != "" || pm.Subject != "long header" {
		t.Errorf("parse error %q, subject %q", pm.ParseError, pm.Subject)
	}
}

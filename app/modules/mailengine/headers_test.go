package mailengine

import (
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

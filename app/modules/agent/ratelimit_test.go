package agent

import (
	"testing"
	"time"
)

const codexUsageLimit = "ERROR: You've hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro), visit https://chatgpt.com/codex/settings/usage to purchase more credits or try again at 7:22 PM.\n"

func TestDetectRateLimitMarkers(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		limited bool
		retry   string
	}{
		{"codex usage limit", "OpenAI Codex v0\nsession id: 1234\n" + codexUsageLimit + codexUsageLimit, true, "7:22 PM"},
		{"rate limit with reset", "error: rate limit exceeded, resets in 3 hours 12 minutes.\n", true, "3 hours 12 minutes"},
		{"http 429", "request failed: HTTP 429 Too Many Requests\n", true, ""},
		{"429 on an error line", "Error: status 429\n", true, ""},
		{"429 elsewhere", "session id: 0429-abcd\nturn 429 finished\n", false, ""},
		{"quota", "insufficient_quota: quota exceeded for this month\n", true, ""},
		{"clean", "codex\n" + sampleReport + "\n", false, ""},
		{"empty", "", false, ""},
	}
	for _, c := range cases {
		got := DetectRateLimitMarkers(c.out)
		if got.Limited != c.limited || got.RetryAfter != c.retry {
			t.Errorf("%s: got %+v want limited=%v retry=%q", c.name, got, c.limited, c.retry)
		}
	}
	if msg := (RateLimitOutcome{Limited: true, RetryAfter: "7:22 PM"}).ErrorMessage(); msg != "usage limit reached (retry after 7:22 PM)" {
		t.Errorf("error message: %q", msg)
	}
	if msg := (RateLimitOutcome{Limited: true}).ErrorMessage(); msg != "usage limit reached" {
		t.Errorf("error message without time: %q", msg)
	}
	if msg := (RateLimitOutcome{}).ErrorMessage(); msg != "" {
		t.Errorf("not limited must render empty, got %q", msg)
	}
}

// hookProvider implements RateLimitDetector with a canned answer.
type hookProvider struct {
	fakeProvider
	outcome RateLimitOutcome
	seen    string
}

func (h *hookProvider) DetectRateLimit(out string) RateLimitOutcome {
	h.seen = out
	return h.outcome
}

func TestDetectRateLimitUsesProviderHook(t *testing.T) {
	var _ RateLimitDetector = codexProvider{}
	if got := detectRateLimit(codexProvider{}, codexUsageLimit); !got.Limited || got.RetryAfter != "7:22 PM" {
		t.Fatalf("codex hook: %+v", got)
	}
	h := &hookProvider{fakeProvider: fakeProvider{name: "hook"}, outcome: RateLimitOutcome{Limited: true, RetryAfter: "tomorrow"}}
	if got := detectRateLimit(h, "anything"); got != h.outcome || h.seen != "anything" {
		t.Fatalf("provider hook not used: %+v seen=%q", got, h.seen)
	}
	if got := detectRateLimit(fakeProvider{name: "plain"}, codexUsageLimit); !got.Limited {
		t.Fatal("providers without a hook fall back to the generic markers")
	}
}

func TestIsUsageLimitMessage(t *testing.T) {
	for _, o := range []RateLimitOutcome{{Limited: true}, {Limited: true, RetryAfter: "7:22 PM"}} {
		if !IsUsageLimitMessage(o.ErrorMessage()) {
			t.Errorf("%q not recognized", o.ErrorMessage())
		}
	}
	if IsUsageLimitMessage("Codex produced no report") || IsUsageLimitMessage("") {
		t.Error("other failures are not usage limits")
	}
}

func TestParseRetryTime(t *testing.T) {
	loc := time.FixedZone("JST", 9*3600)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, loc)
	cases := []struct {
		text string
		want time.Time
		ok   bool
	}{
		{"3:35 PM", time.Date(2026, 9, 28, 15, 35, 0, 0, loc), true},
		{"3:35 PM.", time.Date(2026, 9, 28, 15, 35, 0, 0, loc), true},
		{"7:22 am", time.Date(2026, 9, 29, 7, 22, 0, 0, loc), true}, // already past today: tomorrow
		{"19:22", time.Date(2026, 9, 28, 19, 22, 0, 0, loc), true},
		{"11 PM", time.Date(2026, 9, 28, 23, 0, 0, 0, loc), true},
		{"Sep 30th, 2026 7:22 PM", time.Date(2026, 9, 30, 19, 22, 0, 0, loc), true},
		{"Oct 1 8:05 AM", time.Date(2026, 10, 1, 8, 5, 0, 0, loc), true},
		{"3 hours", now.Add(3 * time.Hour), true},
		{"1 hour 30 minutes", now.Add(90 * time.Minute), true},
		{"15 minutes", now.Add(15 * time.Minute), true},
		{"", time.Time{}, false},
		{"later", time.Time{}, false},
		{"3 hours or so", time.Time{}, false},
	}
	for _, c := range cases {
		got, ok := ParseRetryTime(c.text, now)
		if ok != c.ok || (ok && !got.Equal(c.want)) {
			t.Errorf("ParseRetryTime(%q) = %v, %v; want %v, %v", c.text, got, ok, c.want, c.ok)
		}
	}
	if got := UsageLimitRetryAfter("usage limit reached (retry after 3:35 PM)"); got != "3:35 PM" {
		t.Errorf("UsageLimitRetryAfter = %q", got)
	}
	if got := UsageLimitRetryAfter("usage limit reached"); got != "" {
		t.Errorf("UsageLimitRetryAfter without time = %q", got)
	}
}

func TestFailureSettles(t *testing.T) {
	for msg, want := range map[string]bool{
		"usage limit reached (retry after 3:35 PM)":        false,
		"usage limit reached":                              false,
		"analysis canceled: context canceled":              false,
		CanceledMessage("interrupted by a server restart"): false,
		"Codex produced no report":                         true,
		"Codex timed out after 30m0s":                      true,
		"Codex could not run: exec: not found":             true,
	} {
		if got := FailureSettles(msg); got != want {
			t.Errorf("FailureSettles(%q) = %v, want %v", msg, got, want)
		}
	}
}

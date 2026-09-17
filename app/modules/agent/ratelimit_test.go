package agent

import "testing"

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

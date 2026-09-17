package agent

import (
	"regexp"
	"strings"
)

// RateLimitOutcome says whether a run was refused because of a usage or rate
// limit and, when the CLI said so, when to retry (free text such as
// "7:22 PM" or "3 hours"; "" when unknown).
type RateLimitOutcome struct {
	Limited    bool
	RetryAfter string
}

// ErrorMessage renders the outcome as the error_message stored on the report
// ("usage limit reached (retry after 7:22 PM)"). Empty when not limited.
func (o RateLimitOutcome) ErrorMessage() string {
	if !o.Limited {
		return ""
	}
	if o.RetryAfter == "" {
		return "usage limit reached"
	}
	return "usage limit reached (retry after " + o.RetryAfter + ")"
}

// RateLimitDetector is an optional Provider extension. A provider whose CLI
// reports usage limits in its own way implements it; every other provider is
// checked with the generic markers (DetectRateLimitMarkers).
type RateLimitDetector interface {
	DetectRateLimit(out string) RateLimitOutcome
}

// rateLimitMarkers are lower-case substrings that, in a run that produced no
// usable report, mean the CLI was refused by a usage or rate limit. They are
// matched against the transcript with the echoed prompt removed, so wording
// from the prompt (a rate_limited category, a diagnostic text) cannot trigger
// them.
var rateLimitMarkers = []string{
	"usage limit",
	"usage_limit",
	"rate limit",
	"rate-limit",
	"too many requests",
	"you've hit your",
	"you have hit your",
	"reached your usage",
	"quota exceeded",
	"insufficient_quota",
	"try again at",
}

// http429 matches a status code 429 on a line that also talks about an error
// or a status, so a bare number elsewhere (session ids, times) is ignored.
var http429 = regexp.MustCompile(`(?i)\b429\b`)

// retryAfterRe captures the retry time the CLI announces ("try again at
// 7:22 PM.", "resets in 3 hours", "retry after 15 minutes").
var retryAfterRe = regexp.MustCompile(`(?i)(?:try again|retry|resets?|available again)\s+(?:at|after|in|on)\s+([^.\n]+?)(?:\.(?:\s|$)|\s*$)`)

// DetectRateLimitMarkers is the generic detection used for providers without
// a RateLimitDetector: any marker on any line, or a 429 on an error/status
// line, means the run was limited.
func DetectRateLimitMarkers(out string) RateLimitOutcome {
	for _, line := range strings.Split(out, "\n") {
		low := strings.ToLower(line)
		hit := false
		for _, m := range rateLimitMarkers {
			if strings.Contains(low, m) {
				hit = true
				break
			}
		}
		if !hit && http429.MatchString(line) &&
			(strings.Contains(low, "error") || strings.Contains(low, "status") || strings.Contains(low, "http")) {
			hit = true
		}
		if hit {
			return RateLimitOutcome{Limited: true, RetryAfter: retryAfterText(out)}
		}
	}
	return RateLimitOutcome{}
}

// retryAfterText returns the first retry time announced in out ("" when
// none).
func retryAfterText(out string) string {
	m := retryAfterRe.FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// detectRateLimit runs the provider's own detector when it has one, else the
// generic markers.
func detectRateLimit(p Provider, out string) RateLimitOutcome {
	if d, ok := p.(RateLimitDetector); ok {
		return d.DetectRateLimit(out)
	}
	return DetectRateLimitMarkers(out)
}

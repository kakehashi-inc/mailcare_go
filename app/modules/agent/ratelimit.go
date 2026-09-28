package agent

import (
	"regexp"
	"strconv"
	"strings"
	"time"
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
		return usageLimitPrefix
	}
	return usageLimitPrefix + " (retry after " + o.RetryAfter + ")"
}

// usageLimitPrefix starts every ErrorMessage of a limited run.
const usageLimitPrefix = "usage limit reached"

// IsUsageLimitMessage reports whether a report error message says the run
// was refused by a usage or rate limit (RateLimitOutcome.ErrorMessage).
func IsUsageLimitMessage(msg string) bool {
	return strings.HasPrefix(msg, usageLimitPrefix)
}

// canceledPrefix starts the error message of a run stopped by its caller.
const canceledPrefix = "analysis canceled"

// CanceledMessage is the error message of a run stopped from outside for the
// given reason ("analysis canceled: interrupted by a server restart"); like
// every cancellation it does not settle the group (FailureSettles).
func CanceledMessage(reason string) string {
	return canceledPrefix + ": " + reason
}

// FailureSettles reports whether a failed run with this error message
// settled its group, i.e. the group is unanalyzable as it is and is not
// analyzed again until a bounce of a new pattern arrives: every failure
// except a usage limit and a cancellation (those leave the group waiting).
func FailureSettles(errMsg string) bool {
	return !IsUsageLimitMessage(errMsg) && !strings.HasPrefix(errMsg, canceledPrefix)
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

// usageLimitRetryRe captures the retry text of a usage-limit error message
// ("usage limit reached (retry after 7:22 PM)").
var usageLimitRetryRe = regexp.MustCompile(`^` + usageLimitPrefix + ` \(retry after (.+)\)$`)

// UsageLimitRetryAfter returns the retry text of a usage-limit error message
// ("7:22 PM"; "" when the message names none).
func UsageLimitRetryAfter(msg string) string {
	if m := usageLimitRetryRe.FindStringSubmatch(strings.TrimSpace(msg)); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// retryClockLayouts are the forms of a retry time of day; retryDateLayouts
// the forms that carry the date as well.
var (
	retryClockLayouts = []string{"3:04 PM", "3:04PM", "3 PM", "3PM", "15:04"}
	retryDateLayouts  = []string{
		"Jan 2, 2006 3:04 PM", "Jan 2 2006 3:04 PM", "January 2, 2006 3:04 PM", "January 2 2006 3:04 PM",
		"Jan 2 3:04 PM", "January 2 3:04 PM", "2006-01-02 15:04", "2006-01-02T15:04:05Z07:00",
	}
	retryOrdinalRe  = regexp.MustCompile(`(\d)(st|nd|rd|th)\b`)
	retryDurationRe = regexp.MustCompile(`(?i)(\d+)\s*(days?|d|hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)\b`)
)

// ParseRetryTime converts the retry text of a usage limit into the time the
// limit is lifted, as seen at now (the CLI prints the machine's local time):
// a time of day ("7:22 PM", "19:22") is the next such time after now, a date
// with a time ("Sep 30, 2026 7:22 PM", "Sep 30th 7:22 PM") is taken as it is
// (the year defaulting to now's, or the next one when that has passed), and
// a duration ("3 hours", "1 hour 30 minutes", "15 minutes") is added to now.
// ok is false for anything else.
func ParseRetryTime(text string, now time.Time) (time.Time, bool) {
	text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "."))
	if text == "" {
		return time.Time{}, false
	}
	loc := now.Location()
	norm := strings.Join(strings.Fields(retryOrdinalRe.ReplaceAllString(strings.ReplaceAll(text, ",", ", "), "$1")), " ")
	norm = strings.ReplaceAll(norm, " ,", ",")
	upper := strings.ToUpper(norm)
	for _, layout := range retryClockLayouts {
		t, err := time.ParseInLocation(layout, upper, loc)
		if err != nil {
			continue
		}
		at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		if !at.After(now) {
			at = at.AddDate(0, 0, 1)
		}
		return at, true
	}
	for _, layout := range retryDateLayouts {
		t, err := time.ParseInLocation(layout, norm, loc)
		if err != nil {
			t, err = time.ParseInLocation(layout, upper, loc)
		}
		if err != nil {
			continue
		}
		if !strings.Contains(layout, "2006") {
			t = time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			if t.Before(now.AddDate(0, 0, -1)) {
				t = t.AddDate(1, 0, 0)
			}
		}
		return t, true
	}
	matches := retryDurationRe.FindAllStringSubmatch(norm, -1)
	if len(matches) == 0 || strings.TrimSpace(retryDurationRe.ReplaceAllString(norm, "")) != "" {
		return time.Time{}, false
	}
	var d time.Duration
	for _, m := range matches {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return time.Time{}, false
		}
		switch unit := strings.ToLower(m[2]); {
		case strings.HasPrefix(unit, "d"):
			d += time.Duration(n) * 24 * time.Hour
		case strings.HasPrefix(unit, "h"):
			d += time.Duration(n) * time.Hour
		case strings.HasPrefix(unit, "m"):
			d += time.Duration(n) * time.Minute
		default:
			d += time.Duration(n) * time.Second
		}
	}
	return now.Add(d), true
}

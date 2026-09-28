package mailengine

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strings"

	"mailcare/app/models"
)

// Placeholders and the protected tokens of DiagnosticTemplate. The SMTP reply
// code and the extended status code are swapped for private-use sentinels
// before the generic number replacement and restored afterwards.
var (
	tplStatusCodeRe = regexp.MustCompile(`\b([245]\.[0-9]{1,3}\.[0-9]{1,3})\b`)
	tplSMTPCodeRe   = regexp.MustCompile(`\b([245][0-9]{2})\b`)
	tplEmailRe      = regexp.MustCompile(`<?[a-z0-9._%+\-=/!#$&'*?^{}|~]+@[a-z0-9.\-]+\.[a-z0-9\-]+>?`)
	tplIPv6Re       = regexp.MustCompile(`\[?\b(?:[0-9a-f]{0,4}:){2,7}[0-9a-f]{0,4}\b\]?`)
	tplIPv4Re       = regexp.MustCompile(`\[?\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b\]?`)
	tplHostRe       = regexp.MustCompile(`\b(?:[a-z0-9](?:[a-z0-9\-]{0,62}[a-z0-9])?\.){1,}[a-z][a-z0-9\-]{1,62}\b`)
	tplDateRe       = regexp.MustCompile(strings.Join([]string{
		`\b[0-9]{4}[-/][0-9]{1,2}[-/][0-9]{1,2}(?:[ t][0-9]{1,2}:[0-9]{2}(?::[0-9]{2})?(?:\.[0-9]+)?(?:z|[+\-][0-9]{2}:?[0-9]{2})?)?\b`,
		`\b[0-9]{1,2}[-/][0-9]{1,2}[-/][0-9]{2,4}\b`,
		`\b(?:mon|tue|wed|thu|fri|sat|sun)[a-z]*,?\s+[0-9]{1,2}\s+(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\s+[0-9]{2,4}(?:\s+[0-9]{1,2}:[0-9]{2}(?::[0-9]{2})?)?(?:\s+[+\-][0-9]{4})?`,
		`\b[0-9]{1,2}\s+(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\s+[0-9]{2,4}\b`,
		`\b(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\s+[0-9]{1,2},?\s+[0-9]{2,4}\b`,
		`\b[0-9]{1,2}:[0-9]{2}(?::[0-9]{2})?\b`,
	}, "|"))
	tplIDRe     = regexp.MustCompile(`\b[a-z0-9]{8,}\b`)
	tplHexRe    = regexp.MustCompile(`\b[0-9a-f]{12,}\b`)
	tplNumberRe = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)
	tplSpaceRe  = regexp.MustCompile(`\s+`)
	tplPunctRe  = regexp.MustCompile(`([<>()\[\]{}"'` + "`" + `]+)`)

	sentinelStatus = "\ue000" // private-use code points never occur in mail text
	sentinelSMTP   = "\ue001"
)

// DiagnosticTemplate normalizes a diagnostic text so that notices differing
// only in addresses, hosts, IPs, IDs, dates or numbers collapse to the same
// string (design 5.4). SMTP reply codes (550) and extended status codes
// (5.1.1) are kept verbatim.
func DiagnosticTemplate(diagnostic string) string {
	s := strings.ToLower(strings.TrimSpace(diagnostic))
	if s == "" {
		return ""
	}
	// Protect the codes from the placeholder passes.
	var statuses, smtps []string
	s = tplStatusCodeRe.ReplaceAllStringFunc(s, func(m string) string {
		statuses = append(statuses, m)
		return sentinelStatus
	})
	s = tplEmailRe.ReplaceAllString(s, "<addr>")
	// Dates before IPs: "09:15:30" would otherwise look like an IPv6 address.
	s = tplDateRe.ReplaceAllString(s, "<date>")
	s = tplIPv6Re.ReplaceAllStringFunc(s, func(m string) string {
		if strings.Count(m, ":") < 2 {
			return m
		}
		return "<ip>"
	})
	s = tplIPv4Re.ReplaceAllString(s, "<ip>")
	s = tplHostRe.ReplaceAllString(s, "<host>")
	s = tplSMTPCodeRe.ReplaceAllStringFunc(s, func(m string) string {
		smtps = append(smtps, m)
		return sentinelSMTP
	})
	s = tplHexRe.ReplaceAllString(s, "<id>")
	s = tplIDRe.ReplaceAllStringFunc(s, func(m string) string {
		// Queue IDs and message tokens mix letters and digits; plain words
		// and plain numbers are left for the other passes.
		if !strings.ContainsAny(m, "0123456789") || !strings.ContainsAny(m, "abcdefghijklmnopqrstuvwxyz") {
			return m
		}
		return "<id>"
	})
	s = tplNumberRe.ReplaceAllString(s, "<n>")
	// Restore the protected codes in order.
	for _, v := range statuses {
		s = strings.Replace(s, sentinelStatus, v, 1)
	}
	for _, v := range smtps {
		s = strings.Replace(s, sentinelSMTP, v, 1)
	}
	s = tplPunctRe.ReplaceAllStringFunc(s, func(m string) string {
		// Keep the placeholders' own angle brackets; drop stray quotes.
		return strings.Map(func(r rune) rune {
			switch r {
			case '"', '\'', '`':
				return -1
			}
			return r
		}, m)
	})
	// The template is kept whole (no length cap): a long diagnostic keeps
	// its full shape so that notices differing late in the text stay apart.
	return strings.TrimSpace(tplSpaceRe.ReplaceAllString(s, " "))
}

// groupKeyPart normalizes one component of the group identity (design 5.4:
// lower-cased, trimmed).
func groupKeyPart(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

// GroupKey is the first 16 hex digits of sha1(category|unit_value|authority);
// unit_value and authority are compared lower-cased and trimmed.
func GroupKey(category, unitValue, authority string) string {
	sum := sha1.Sum([]byte(groupKeyPart(category) + "|" + groupKeyPart(unitValue) + "|" + groupKeyPart(authority)))
	return hex.EncodeToString(sum[:])[:16]
}

// PatternKey is the first 16 hex digits of
// sha1(status_code|pattern template|remote_mta|source kind): bounces of a
// group that share it are the same pattern (same status, same wording, same
// remote MTA, diagnostic read from the same kind of place). The agent
// samples one notice per pattern, and a group is analyzed again only when a
// pattern appears that its latest completed report did not cover. The
// pattern template is the diagnostic template with the numbers that
// directly follow an <id> placeholder also replaced (the tail of a queue ID
// such as "<id>-<id>.218" is kept verbatim by DiagnosticTemplate when it
// looks like an SMTP reply code, which would split one pattern per notice).
func PatternKey(statusCode, diagnosticTemplate, remoteMTA, diagnosticSource string) string {
	template := patternIDTailRe.ReplaceAllString(groupKeyPart(diagnosticTemplate), "$1<n>")
	sum := sha1.Sum([]byte(groupKeyPart(statusCode) + "|" + template + "|" +
		groupKeyPart(remoteMTA) + "|" + SourceKind(diagnosticSource)))
	return hex.EncodeToString(sum[:])[:16]
}

// patternIDTailRe matches a number right after an <id> placeholder and its
// separator ("<id>.218", "<id>-5").
var patternIDTailRe = regexp.MustCompile(`(<id>[.\-])[0-9]+\b`)

// groupForBounce categorizes a bounce and builds the group row it belongs
// to (the responsible party is a column of the group only); the name of the
// category rule that matched is recorded on the bounce (CategoryRule). Only
// failed and delayed notices belong to a group (isGroupedKind); anything
// else is nil.
func groupForBounce(kind string, b *models.Bounce) *models.BounceGroup {
	if !isGroupedKind(kind) || b == nil {
		return nil
	}
	c := Categorize(b, kind)
	b.CategoryRule = c.Reason
	return &models.BounceGroup{
		GroupKey:           GroupKey(c.Category, c.UnitValue, c.Authority),
		Category:           c.Category,
		UnitValue:          groupKeyPart(c.UnitValue),
		Authority:          groupKeyPart(c.Authority),
		Actionable:         c.Actionable,
		RecipientDomain:    b.RecipientDomain,
		StatusCode:         b.StatusCode,
		DiagnosticTemplate: b.DiagnosticTemplate,
		Responsible:        c.Responsible,
	}
}

// groupTracker collects the groups touched during one run and decides, for
// every message, whether its group must be flagged for analysis: an
// actionable group is flagged when the bounce just filed into it has a
// pattern (bounces.pattern_key) that the latest completed report of the
// group did not cover (always the case for a group without a completed
// report, a new group included). More notices of a covered pattern only
// update the counters (design 5.4 "incremental"). The counters and the flag
// are written inside the transaction of the message, so they are always in
// step with the bounces rows. At the end the flagged groups are reported as
// GroupResult.GroupsTouched.
type groupTracker struct {
	order      []string
	seen       map[string]bool
	actionable map[string]bool // groups.actionable as written by the last upsert
	flagged    map[string]bool // actionable groups flagged during the run
}

func newGroupTracker() *groupTracker {
	return &groupTracker{seen: map[string]bool{}, actionable: map[string]bool{}, flagged: map[string]bool{}}
}

// upsert stores the descriptive columns of the group and remembers its key.
func (t *groupTracker) upsert(db models.Execer, g *models.BounceGroup) error {
	if g == nil {
		return nil
	}
	if !t.seen[g.GroupKey] {
		t.seen[g.GroupKey] = true
		t.order = append(t.order, g.GroupKey)
	}
	if err := models.UpsertGroup(db, g); err != nil {
		return err
	}
	t.actionable[g.GroupKey] = g.Actionable
	return nil
}

// recount refreshes the counters of the group after the bounces row of the
// current message was written and, when the group is actionable and the
// pattern of that bounce is not covered by the latest completed report of
// the group, sets needs_analysis (once per run; recipient-side groups are
// never flagged).
func (t *groupTracker) recount(db models.Execer, g *models.BounceGroup, patternKey string) error {
	if g == nil {
		return nil
	}
	if err := models.RefreshGroupCounters(db, g.GroupKey); err != nil {
		return err
	}
	if !t.actionable[g.GroupKey] || t.flagged[g.GroupKey] {
		return nil
	}
	covered, err := models.PatternCovered(db, g.GroupKey, patternKey)
	if err != nil {
		return err
	}
	if covered {
		return nil
	}
	if err := models.SetGroupNeedsAnalysis(db, g.GroupKey, true); err != nil {
		return err
	}
	t.flagged[g.GroupKey] = true
	return nil
}

// touched returns the keys of the actionable groups flagged for analysis
// during the run, in first-seen order (GroupResult.GroupsTouched).
func (t *groupTracker) touched() []string {
	touched := []string{}
	for _, key := range t.order {
		if t.flagged[key] {
			touched = append(touched, key)
		}
	}
	return touched
}

package mailengine

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
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

// groupForBounce categorizes a bounce and builds the group row it belongs
// to (the responsible party is a column of the group only). Only failed and
// delayed notices belong to a group (isGroupedKind); anything else is nil.
func groupForBounce(kind string, b *models.Bounce) *models.BounceGroup {
	if !isGroupedKind(kind) || b == nil {
		return nil
	}
	c := Categorize(b, kind)
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
// actionable group is flagged as soon as its message count exceeds the
// count it had before the run (design 5.4 "incremental"). The counters and
// the flag are written inside the transaction of the message, so they are
// always in step with the bounces rows. At the end the flagged groups are
// reported as GroupResult.GroupsTouched.
type groupTracker struct {
	order      []string
	seen       map[string]bool
	before     map[string]int  // message_count before the run (0 for new groups)
	actionable map[string]bool // groups.actionable as written by the last upsert
	grew       map[string]bool // actionable groups flagged during the run
}

func newGroupTracker() *groupTracker {
	return &groupTracker{seen: map[string]bool{}, before: map[string]int{}, actionable: map[string]bool{}, grew: map[string]bool{}}
}

// upsert stores the descriptive columns of the group and remembers its key
// together with its message count before the run.
func (t *groupTracker) upsert(db models.Execer, g *models.BounceGroup) error {
	if g == nil {
		return nil
	}
	if !t.seen[g.GroupKey] {
		var count int
		err := db.QueryRow(`SELECT message_count FROM groups WHERE group_key = ?`, g.GroupKey).Scan(&count)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		t.seen[g.GroupKey] = true
		t.before[g.GroupKey] = count
		t.order = append(t.order, g.GroupKey)
	}
	if err := models.UpsertGroup(db, g); err != nil {
		return err
	}
	t.actionable[g.GroupKey] = g.Actionable
	return nil
}

// recount refreshes the counters of the group after the bounces row of the
// current message was written and, when the group is actionable and its
// message count now exceeds the count before the run, sets needs_analysis
// (once per run; recipient-side groups are never flagged).
func (t *groupTracker) recount(db models.Execer, g *models.BounceGroup) error {
	if g == nil {
		return nil
	}
	if err := models.RefreshGroupCounters(db, g.GroupKey); err != nil {
		return err
	}
	if !t.actionable[g.GroupKey] || t.grew[g.GroupKey] {
		return nil
	}
	var count int
	if err := db.QueryRow(`SELECT message_count FROM groups WHERE group_key = ?`, g.GroupKey).Scan(&count); err != nil {
		return err
	}
	if count <= t.before[g.GroupKey] {
		return nil
	}
	if err := models.SetGroupNeedsAnalysis(db, g.GroupKey, true); err != nil {
		return err
	}
	t.grew[g.GroupKey] = true
	return nil
}

// touched returns the keys of the actionable groups whose message count
// grew during the run, in first-seen order (GroupResult.GroupsTouched).
func (t *groupTracker) touched() []string {
	touched := []string{}
	for _, key := range t.order {
		if t.grew[key] {
			touched = append(touched, key)
		}
	}
	return touched
}

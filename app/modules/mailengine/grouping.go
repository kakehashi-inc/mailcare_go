package mailengine

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode/utf8"

	"mailcare/app/models"
)

// maxTemplateLen caps the diagnostic template (in runes).
const maxTemplateLen = 300

// templateTitleLen is how much of the template goes into a group title.
const templateTitleLen = 80

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
	s = strings.TrimSpace(tplSpaceRe.ReplaceAllString(s, " "))
	if utf8.RuneCountInString(s) > maxTemplateLen {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:maxTemplateLen]))
	}
	return s
}

// GroupKey is the first 16 hex digits of sha1(kind|domain|status|template).
func GroupKey(bounceKind, recipientDomain, statusCode, template string) string {
	sum := sha1.Sum([]byte(bounceKind + "|" + recipientDomain + "|" + statusCode + "|" + template))
	return hex.EncodeToString(sum[:])[:16]
}

// GroupTitle renders "<domain> - <status> - <template head>" leaving out the
// parts that are unknown.
func GroupTitle(recipientDomain, statusCode, template string) string {
	var parts []string
	if recipientDomain != "" {
		parts = append(parts, recipientDomain)
	}
	if statusCode != "" {
		parts = append(parts, statusCode)
	}
	if template != "" {
		head := template
		if utf8.RuneCountInString(head) > templateTitleLen {
			head = strings.TrimSpace(string([]rune(head)[:templateTitleLen])) + "..."
		}
		parts = append(parts, head)
	}
	if len(parts) == 0 {
		return "(no diagnostic)"
	}
	return strings.Join(parts, " - ")
}

// groupForBounce builds the group row a bounce belongs to. Non-bounces and
// auto-replies do not belong to any group (nil).
func groupForBounce(kind string, b *models.Bounce) *models.BounceGroup {
	switch kind {
	case bounceKindFailed, bounceKindDelayed, bounceKindOther:
	default:
		return nil
	}
	if b == nil {
		return nil
	}
	key := GroupKey(kind, b.RecipientDomain, b.StatusCode, b.DiagnosticTemplate)
	return &models.BounceGroup{
		GroupKey:           key,
		Title:              GroupTitle(b.RecipientDomain, b.StatusCode, b.DiagnosticTemplate),
		BounceKind:         kind,
		RecipientDomain:    b.RecipientDomain,
		StatusCode:         b.StatusCode,
		SMTPCode:           b.SMTPCode,
		DiagnosticTemplate: b.DiagnosticTemplate,
		Responsible:        b.Responsible,
	}
}

// groupTracker collects the groups touched during one run so that their
// counters are refreshed once at the end (design 5.4 "incremental").
type groupTracker struct {
	order []string
	seen  map[string]bool
}

func newGroupTracker() *groupTracker {
	return &groupTracker{seen: map[string]bool{}}
}

// upsert stores the descriptive columns of the group and remembers its key.
func (t *groupTracker) upsert(db *sql.DB, g *models.BounceGroup) error {
	if g == nil {
		return nil
	}
	if err := models.UpsertGroup(db, g); err != nil {
		return err
	}
	if !t.seen[g.GroupKey] {
		t.seen[g.GroupKey] = true
		t.order = append(t.order, g.GroupKey)
	}
	return nil
}

// refresh recomputes the counters of every touched group and returns the
// touched keys in first-seen order.
func (t *groupTracker) refresh(db *sql.DB) ([]string, error) {
	for _, key := range t.order {
		if err := models.RefreshGroupCounters(db, key); err != nil {
			return nil, err
		}
	}
	return append([]string(nil), t.order...), nil
}

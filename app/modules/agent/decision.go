package agent

import (
	"strings"
	"time"

	"mailcare/app/models"
)

// Decision is the user's decision on a group that the prompt carries: the
// one being re-checked (the group is resolved or ignored: the prompt asks
// whether it still holds) or an earlier one (the group was reopened after
// it: the prompt gives it as background).
type Decision struct {
	State     string    // resolved | ignored
	DecidedAt time.Time // when the state was set
	// Reason is the English description of the chosen code ("" when none was
	// recorded) and Other is true when the code was "other" (the note then
	// holds the reason). Note is the user's text as written ("" when none).
	Reason string
	Other  bool
	Note   string
	// ReopenedAt is when the group was reopened after the decision (zero for
	// the decision being re-checked).
	ReopenedAt time.Time
	// SettlingDays is how many days after a resolved decision notices about
	// mail sent meanwhile are expected (the category's re-check days; 0 for
	// an ignored group).
	SettlingDays int
}

// resolveActionDescriptions and ignoreReasonDescriptions are the English
// meaning of the codes a user chooses (models.ResolveActions,
// models.IgnoreReasons) as the prompt states them.
var resolveActionDescriptions = map[string]string{
	models.ResolveActionDelisting:      "requested delisting of the sending IP from the blacklist",
	models.ResolveActionDNSFixed:       "corrected the DNS records of the sending domain (SPF, DKIM, DMARC)",
	models.ResolveActionServerFixed:    "corrected the configuration of the sending mail server",
	models.ResolveActionSenderChanged:  "reviewed the sender address",
	models.ResolveActionContentChanged: "reviewed the content or the attachments of the mail",
	models.ResolveActionVolumeAdjusted: "adjusted the sending volume or rate",
	models.ResolveActionRecipientFixed: "corrected or removed the recipient address",
	models.ResolveActionRecipientAsked: "contacted the administrator on the recipient side",
	models.ResolveActionOther:          "other (see the details)",
}

var ignoreReasonDescriptions = map[string]string{
	models.IgnoreReasonTemporary:       "a temporary problem that has already cleared; no action needed",
	models.IgnoreReasonRecipientSide:   "a problem on the recipient side; the sending side cannot act",
	models.IgnoreReasonInputError:      "the recipient address was entered wrongly by a user of the sending system; the sending side cannot act",
	models.IgnoreReasonStoppedSending:  "mail to the recipient is no longer sent; no action needed",
	models.IgnoreReasonSpoofing:        "sent by a third party using the monitored domain; the sending side cannot act",
	models.IgnoreReasonExternalService: "a problem of an external service used for sending; the sending side cannot act",
	models.IgnoreReasonFalsePositive:   "detected by mistake; no action needed",
	models.IgnoreReasonLowImpact:       "the impact is small; no action needed",
	models.IgnoreReasonTestMail:        "mail sent for a test or a check; no action needed",
	models.IgnoreReasonOther:           "other (see the details)",
}

// decisionFrom converts a state change into a Decision.
func decisionFrom(c *models.GroupStateChange) *Decision {
	d := &Decision{State: c.State, DecidedAt: c.ChangedAt.UTC(), Note: strings.TrimSpace(c.Note.String)}
	if c.Reason.Valid {
		d.Other = c.Reason.String == models.ResolveActionOther || c.Reason.String == models.IgnoreReasonOther
		if c.State == "resolved" {
			d.Reason = resolveActionDescriptions[c.Reason.String]
		} else {
			d.Reason = ignoreReasonDescriptions[c.Reason.String]
		}
	}
	return d
}

// decisionsOf picks the decisions the prompt of a group carries from its
// history (newest first): for a resolved or ignored group the latest state
// change (being re-checked); for an open group the latest resolved or
// ignored change followed by a reopening (an earlier decision). Both are nil
// when the history has none.
func decisionsOf(g *models.BounceGroup, history []*models.GroupStateChange) (recheck, earlier *Decision) {
	if len(history) == 0 {
		return nil, nil
	}
	if g.State != "open" {
		if history[0].State == g.State {
			return decisionFrom(history[0]), nil
		}
		return nil, nil
	}
	for i := 1; i < len(history); i++ {
		if history[i].State != "open" {
			d := decisionFrom(history[i])
			d.ReopenedAt = history[i-1].ChangedAt.UTC()
			return nil, d
		}
	}
	return nil, nil
}

// reportBefore returns the newest completed report of a group finished at or
// before t (nil when there is none): the analysis a decision was based on.
func reportBefore(reports []*models.AgentReport, t time.Time) *models.AgentReport {
	for _, r := range reports {
		if r.Status == "completed" && r.FinishedAt.Valid && !r.FinishedAt.Time.After(t) {
			return r
		}
	}
	return nil
}

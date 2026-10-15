package modules

import (
	"database/sql"
	"errors"
	"log"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/agent"
	"mailcare/app/modules/dnscheck"
	"mailcare/app/modules/mailengine"
)

// dnsCheckLookup returns the callback through which the analysis shows the
// DNS verification of a sample notice (agent.AnalyzeInput.DNSCheck). It only
// reads dns_checks and looks nothing up, so it is passed whether the check is
// switched on or not: the stored outcomes still explain the classification.
func (m *JobManager) dnsCheckLookup() agent.DNSCheckFunc {
	return func(name, qtype string, noticeTime time.Time) (agent.DNSCheckResult, bool) {
		return storedDNSCheck(m.db, name, qtype, noticeTime, time.Now())
	}
}

// storedDNSCheck picks the stored verification a notice uses, by the reuse
// rule of design document "mail classification" 4.4 that dnscheck applies
// when grouping: the same name and record type (none named = MX), the
// earliest checked within the window of the notice (dnscheck.ReuseRange:
// from 24 hours before it was received to 48 hours after, a notice time
// that is zero or after now counting as now). Only the outcomes exists /
// not_found / broken count; a database error is logged and leaves the name
// unverified, never failing the analysis.
func storedDNSCheck(db *sql.DB, name, qtype string, noticeTime, now time.Time) (agent.DNSCheckResult, bool) {
	if qtype == "" {
		qtype = "MX"
	}
	from, to := dnscheck.ReuseRange(noticeTime, now)
	row, err := models.FindDNSCheck(db, name, qtype, from, to)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("analyze: failed to read the dns check of %s %s: %v", name, qtype, err)
		}
		return agent.DNSCheckResult{}, false
	}
	switch mailengine.DNSOutcome(row.Outcome) {
	case mailengine.DNSOutcomeExists, mailengine.DNSOutcomeNotFound, mailengine.DNSOutcomeBroken:
		return agent.DNSCheckResult{Outcome: row.Outcome, Detail: row.Detail, CheckedAt: row.CheckedAt}, true
	}
	return agent.DNSCheckResult{}, false
}

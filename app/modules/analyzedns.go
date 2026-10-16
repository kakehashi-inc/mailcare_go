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
// Whether the check is on is read when the analysis asks.
func (m *JobManager) dnsCheckLookup() agent.DNSCheckFunc {
	return func(name string, noticeTime time.Time) (agent.DNSCheckResult, bool) {
		return storedDNSCheck(m.db, name, noticeTime, time.Now(), ResolveDNSCheckEnabled(m.db))
	}
}

// storedDNSCheck picks the stored verification a notice uses, by the reuse
// rule of design document "mail classification" 4.4 that dnscheck applies
// when grouping, for the same name (whatever record type the notice named):
// while the check is on (live), the earliest checked within
// dnscheck.CacheFreshAge before now, else the latest stored one (the one a
// verification made since stored, or the stand-in of one that gave
// nothing); while it is off, the earliest checked within the window of the
// notice (dnscheck.ReuseRange: from 24 hours before it was received up to
// now, a notice time that is zero or after now counting as now). Either
// kind counts, whether the authoritative servers were asked or not. A
// notice that showed mail from the sending sources may have used another
// row of a verification with them (the grouping prefers those for it),
// which the analysis cannot tell without the sending sources of the run;
// the result says which kind it shows (Direct). Only the outcomes exists /
// not_found / broken count; a database error is logged and leaves the name
// unverified, never failing the analysis.
func storedDNSCheck(db *sql.DB, name string, noticeTime, now time.Time, live bool) (agent.DNSCheckResult, bool) {
	var row *models.DNSCheck
	var err error
	if live {
		row, err = models.FindDNSCheck(db, name, dnscheck.VerifyKind, now.Add(-dnscheck.CacheFreshAge), now, false)
		if errors.Is(err, sql.ErrNoRows) {
			row, err = models.LatestDNSCheck(db, name, dnscheck.VerifyKind)
		}
	} else {
		from, to := dnscheck.ReuseRange(noticeTime, now)
		row, err = models.FindDNSCheck(db, name, dnscheck.VerifyKind, from, to, false)
	}
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("analyze: failed to read the dns check of %s: %v", name, err)
		}
		return agent.DNSCheckResult{}, false
	}
	switch mailengine.DNSOutcome(row.Outcome) {
	case mailengine.DNSOutcomeExists, mailengine.DNSOutcomeNotFound, mailengine.DNSOutcomeBroken:
		return agent.DNSCheckResult{Outcome: row.Outcome, Detail: row.Detail, Direct: row.Direct, CheckedAt: row.CheckedAt}, true
	}
	return agent.DNSCheckResult{}, false
}

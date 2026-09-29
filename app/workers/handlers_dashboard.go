package workers

import (
	"log"
	"net/http"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules"
	"mailcare/app/modules/agent"
)

const dashboardRecentJobs = 10

// nextCheckAt returns the next scheduled check as an RFC 3339 string.
func (c *core) nextCheckAt() *string {
	next, ok := modules.NextCheckAt(time.Now(), modules.ResolveCheckTimes(c.db))
	if !ok {
		return nil
	}
	s := timeString(next)
	return &s
}

// handleDashboard aggregates every mailbox: counters, the busy mailboxes, the active and recent jobs
// (administrators only; empty for members), the next check and the agent
// status. A mailbox whose index cannot be opened contributes zeros (logged).
func (c *core) handleDashboard(w http.ResponseWriter, r *http.Request) {
	mailboxes, err := models.ListMailboxes(c.db)
	if err != nil {
		writeInternalError(w, "failed to list mailboxes", err)
		return
	}
	dto := DashboardDTO{
		Mailboxes:   make([]MailboxDTO, 0, len(mailboxes)),
		CheckTimes:  modules.ResolveCheckTimes(c.db),
		NextCheckAt: c.nextCheckAt(),
	}
	dto.Totals.Mailboxes = len(mailboxes)
	u := userFrom(r)
	for _, mb := range mailboxes {
		mdto := mailboxDTOFor(mb, u)
		mdto.Stats = &MailboxStatsDTO{}
		idx, err := c.openIndex(r, mb)
		if err != nil {
			log.Printf("index of %s could not be opened: %v", mb.Address, err)
			dto.Mailboxes = append(dto.Mailboxes, mdto)
			continue
		}
		mdto.Stats = indexStats(idx, mb)
		dto.Totals.Messages += mdto.Stats.Messages
		dto.Totals.TargetMessages += mdto.Stats.TargetMessages
		dto.Totals.JunkMessages += mdto.Stats.JunkMessages
		dto.Totals.Unclassified += mdto.Stats.Unclassified
		dto.Totals.OpenGroups += mdto.Stats.Groups.Open
		dto.Totals.RecheckGroups += mdto.Stats.Groups.ResolvedRecheck + mdto.Stats.Groups.IgnoredRecheck
		idx.Close()
		dto.Mailboxes = append(dto.Mailboxes, mdto)
	}

	// Jobs are shown to administrators only; members get empty lists. Every
	// user sees which mailboxes are busy (a job on their mails is queued or
	// running).
	active, err := models.ListActiveJobs(c.db)
	if err != nil {
		writeInternalError(w, "failed to list jobs", err)
		return
	}
	dto.BusyMailboxIDs = busyMailboxIDs(active, mailboxes)
	dto.ActiveJobs, dto.RecentJobs = []JobDTO{}, []JobDTO{}
	if u != nil && u.Role == modules.RoleAdmin {
		addresses := c.mailboxAddresses()
		dto.ActiveJobs = toJobDTOs(active, addresses)
		recentJobs, err := models.ListJobs(c.db, dashboardRecentJobs)
		if err != nil {
			writeInternalError(w, "failed to list jobs", err)
			return
		}
		dto.RecentJobs = toJobDTOs(recentJobs, addresses)
	}

	provider := modules.ResolveAgentProvider(c.db)
	dto.Agent = DashboardAgentDTO{Provider: provider, Enabled: modules.ResolveAgentEnabled(c.db),
		Available: agent.ProviderAvailable(provider)}
	writeJSON(w, http.StatusOK, dto)
}

// mailboxJobKinds are the job kinds that work on the mails of a mailbox: a
// mailbox is busy while one of them is queued or running for it (or for
// every mailbox).
var mailboxJobKinds = map[string]bool{
	modules.JobKindSync: true, modules.JobKindFetch: true, modules.JobKindGroup: true,
	modules.JobKindReindex: true, modules.JobKindReclassify: true, modules.JobKindCleanup: true,
}

// busyMailboxIDs returns the ids of the mailboxes that an active job of
// mailboxJobKinds works on, in mailbox order.
func busyMailboxIDs(active []*models.Job, mailboxes []*models.Mailbox) []int64 {
	all := false
	busy := map[int64]bool{}
	for _, j := range active {
		if !mailboxJobKinds[j.Kind] {
			continue
		}
		if !j.MailboxID.Valid {
			all = true
			continue
		}
		busy[j.MailboxID.Int64] = true
	}
	ids := []int64{}
	for _, mb := range mailboxes {
		if all || busy[mb.ID] {
			ids = append(ids, mb.ID)
		}
	}
	return ids
}

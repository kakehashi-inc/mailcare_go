package workers

import (
	"log"
	"net/http"
	"sort"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules"
	"mailcare/app/modules/agent"
)

const (
	dashboardRecentGroups = 10
	dashboardRecentJobs   = 10
)

// nextCheckAt returns the next scheduled check as an RFC 3339 string.
func (c *core) nextCheckAt() *string {
	next, ok := modules.NextCheckAt(time.Now(), modules.ResolveCheckTimes(c.db))
	if !ok {
		return nil
	}
	s := timeString(next)
	return &s
}

// handleDashboard aggregates every mailbox: counters, the newest open groups,
// the active and recent jobs, the next check and the agent status. A mailbox
// whose index cannot be opened contributes zeros (logged).
func (c *core) handleDashboard(w http.ResponseWriter, r *http.Request) {
	mailboxes, err := models.ListMailboxes(c.db)
	if err != nil {
		writeInternalError(w, "failed to list mailboxes", err)
		return
	}
	dto := DashboardDTO{
		Mailboxes:    make([]MailboxDTO, 0, len(mailboxes)),
		RecentGroups: []DashboardGroupDTO{},
		CheckTimes:   modules.ResolveCheckTimes(c.db),
		NextCheckAt:  c.nextCheckAt(),
	}
	dto.Totals.Mailboxes = len(mailboxes)
	type recent struct {
		DashboardGroupDTO
		lastSeen time.Time
	}
	var recents []recent
	for _, mb := range mailboxes {
		mdto := toMailboxDTO(mb)
		mdto.Stats = &MailboxStatsDTO{}
		idx, err := c.openIndex(r, mb)
		if err != nil {
			log.Printf("index of %s could not be opened: %v", mb.Address, err)
			dto.Mailboxes = append(dto.Mailboxes, mdto)
			continue
		}
		if mdto.Stats.Messages, mdto.Stats.Bounces, err = models.CountMessages(idx); err != nil {
			log.Printf("failed to count messages of %s: %v", mb.Address, err)
		}
		if mdto.Stats.Groups, err = models.CountGroups(idx); err != nil {
			log.Printf("failed to count groups of %s: %v", mb.Address, err)
		}
		dto.Totals.Messages += mdto.Stats.Messages
		dto.Totals.Bounces += mdto.Stats.Bounces
		dto.Totals.OpenGroups += mdto.Stats.Groups.Open
		groups, err := models.ListGroups(idx, models.GroupFilter{State: modules.GroupStateOpen})
		if err != nil {
			log.Printf("failed to list groups of %s: %v", mb.Address, err)
		} else {
			if len(groups) > dashboardRecentGroups {
				groups = groups[:dashboardRecentGroups]
			}
			gdtos, err := groupDTOs(idx, groups)
			if err != nil {
				log.Printf("failed to load reports of %s: %v", mb.Address, err)
			} else {
				for i, g := range gdtos {
					recents = append(recents, recent{
						DashboardGroupDTO: DashboardGroupDTO{GroupDTO: g, MailboxID: mb.ID, MailboxAddress: mb.Address},
						lastSeen:          groups[i].LastSeen.Time,
					})
				}
			}
		}
		idx.Close()
		dto.Mailboxes = append(dto.Mailboxes, mdto)
	}
	sort.SliceStable(recents, func(i, j int) bool { return recents[i].lastSeen.After(recents[j].lastSeen) })
	if len(recents) > dashboardRecentGroups {
		recents = recents[:dashboardRecentGroups]
	}
	for _, g := range recents {
		dto.RecentGroups = append(dto.RecentGroups, g.DashboardGroupDTO)
	}

	addresses := c.mailboxAddresses()
	active, err := models.ListActiveJobs(c.db)
	if err != nil {
		writeInternalError(w, "failed to list jobs", err)
		return
	}
	dto.ActiveJobs = toJobDTOs(active, addresses)
	recentJobs, err := models.ListJobs(c.db, dashboardRecentJobs)
	if err != nil {
		writeInternalError(w, "failed to list jobs", err)
		return
	}
	dto.RecentJobs = toJobDTOs(recentJobs, addresses)

	provider := modules.ResolveAgentProvider(c.db)
	dto.Agent = DashboardAgentDTO{Provider: provider, Enabled: modules.ResolveAgentEnabled(c.db),
		Available: agent.ProviderAvailable(provider)}
	writeJSON(w, http.StatusOK, dto)
}

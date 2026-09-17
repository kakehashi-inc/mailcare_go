package workers

import (
	"database/sql"
	"net/http"
	"regexp"
	"strings"

	"mailcare/app/models"
	"mailcare/app/modules"
)

// groupKeyRe bounds the shape of a group key accepted in a path.
var groupKeyRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// groupFromPath opens the index of the mailbox and loads the group named by
// {key}. The caller closes the returned index.
func (c *core) groupFromPath(w http.ResponseWriter, r *http.Request) (*models.Mailbox, *sql.DB, *models.BounceGroup, bool) {
	mb, ok := c.mailboxFromPath(w, r)
	if !ok {
		return nil, nil, nil, false
	}
	key := r.PathValue("key")
	if !groupKeyRe.MatchString(key) {
		writeError(w, http.StatusBadRequest, "invalid group key")
		return nil, nil, nil, false
	}
	idx, err := c.openIndex(r, mb)
	if err != nil {
		writeInternalError(w, "failed to open the mail index", err)
		return nil, nil, nil, false
	}
	g, err := models.GetGroup(idx, key)
	if err == sql.ErrNoRows {
		idx.Close()
		writeError(w, http.StatusNotFound, "group not found")
		return nil, nil, nil, false
	}
	if err != nil {
		idx.Close()
		writeInternalError(w, "failed to load group", err)
		return nil, nil, nil, false
	}
	return mb, idx, g, true
}

// groupDTOs converts groups with their report headlines.
func groupDTOs(idx *sql.DB, groups []*models.BounceGroup) ([]GroupDTO, error) {
	completed, err := models.LatestCompletedAgentReports(idx)
	if err != nil {
		return nil, err
	}
	out := make([]GroupDTO, 0, len(groups))
	for _, g := range groups {
		latest, err := models.LatestAgentReport(idx, g.GroupKey)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		out = append(out, toGroupDTO(g, completed[g.GroupKey], latest))
	}
	return out, nil
}

func (c *core) handleListGroups(w http.ResponseWriter, r *http.Request) {
	mb, ok := c.mailboxFromPath(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	filter := models.GroupFilter{State: q.Get("state"), Responsible: q.Get("responsible"), Category: q.Get("category"),
		Query: q.Get("q")}
	actionable, ok := modules.ActionableFilter(q.Get("scope"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid scope")
		return
	}
	filter.Actionable = actionable
	if filter.Category != "" && !modules.IsKnownCategory(filter.Category) {
		writeError(w, http.StatusBadRequest, "invalid category")
		return
	}
	switch filter.State {
	case "", modules.GroupStateOpen, modules.GroupStateResolved, modules.GroupStateIgnored:
	default:
		writeError(w, http.StatusBadRequest, "invalid state")
		return
	}
	switch filter.Responsible {
	case "", modules.ResponsibleSender, modules.ResponsibleRecipient, modules.ResponsibleDomain, modules.ResponsibleUnknown:
	default:
		writeError(w, http.StatusBadRequest, "invalid responsible")
		return
	}
	idx, err := c.openIndex(r, mb)
	if err != nil {
		writeInternalError(w, "failed to open the mail index", err)
		return
	}
	defer idx.Close()
	groups, err := models.ListGroups(idx, filter)
	if err != nil {
		writeInternalError(w, "failed to list groups", err)
		return
	}
	out, err := groupDTOs(idx, groups)
	if err != nil {
		writeInternalError(w, "failed to load reports", err)
		return
	}
	counts, err := models.CountGroups(idx, actionable)
	if err != nil {
		writeInternalError(w, "failed to count groups", err)
		return
	}
	excluded := false
	excludedCounts, err := models.CountGroups(idx, &excluded)
	if err != nil {
		writeInternalError(w, "failed to count excluded groups", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out, "counts": counts,
		"excluded_count": excludedCounts.Open + excludedCounts.Resolved + excludedCounts.Ignored})
}

func (c *core) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	_, idx, g, ok := c.groupFromPath(w, r)
	if !ok {
		return
	}
	defer idx.Close()
	stats, err := models.GroupStats(idx, g.GroupKey)
	if err != nil {
		writeInternalError(w, "failed to load group statistics", err)
		return
	}
	reports, err := models.ListAgentReports(idx, g.GroupKey)
	if err != nil {
		writeInternalError(w, "failed to list reports", err)
		return
	}
	var completed, latest *models.AgentReport
	if len(reports) > 0 {
		latest = reports[0]
	}
	for _, rep := range reports {
		if rep.Status == "completed" {
			completed = rep
			break
		}
	}
	messages, _, err := models.ListMessages(idx, models.MessageFilter{GroupKey: g.GroupKey, Limit: 200})
	if err != nil {
		writeInternalError(w, "failed to list messages", err)
		return
	}
	reportDTOs := make([]ReportDTO, 0, len(reports))
	for _, rep := range reports {
		reportDTOs = append(reportDTOs, toReportDTO(rep))
	}
	var report *ReportDTO
	if completed != nil {
		dto := toReportDTO(completed)
		report = &dto
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"group": toGroupDTO(g, completed, latest), "stats": stats, "report": report,
		"reports": reportDTOs, "messages": toMessageDTOs(messages),
	})
}

func (c *core) handleSetGroupState(w http.ResponseWriter, r *http.Request) {
	var body struct {
		State string `json:"state"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.State = strings.TrimSpace(body.State)
	switch body.State {
	case modules.GroupStateOpen, modules.GroupStateResolved, modules.GroupStateIgnored:
	default:
		writeError(w, http.StatusBadRequest, "invalid state")
		return
	}
	_, idx, g, ok := c.groupFromPath(w, r)
	if !ok {
		return
	}
	defer idx.Close()
	if err := models.SetGroupState(idx, g.GroupKey, body.State); err != nil {
		writeInternalError(w, "failed to update group", err)
		return
	}
	fresh, err := models.GetGroup(idx, g.GroupKey)
	if err != nil {
		writeInternalError(w, "failed to reload group", err)
		return
	}
	completed, _ := models.LatestCompletedAgentReport(idx, g.GroupKey)
	latest, _ := models.LatestAgentReport(idx, g.GroupKey)
	writeJSON(w, http.StatusOK, map[string]any{"group": toGroupDTO(fresh, completed, latest)})
}

func (c *core) handleAnalyzeGroup(w http.ResponseWriter, r *http.Request) {
	mb, idx, g, ok := c.groupFromPath(w, r)
	if !ok {
		return
	}
	idx.Close()
	c.enqueueAndRespond(w, r, modules.JobKindAnalyze, mb.ID, g.GroupKey)
}

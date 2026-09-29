package workers

import (
	"database/sql"
	"net/http"
	"regexp"
	"strconv"
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
		writeError(w, http.StatusBadRequest, "system.invalidRequest")
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
		writeError(w, http.StatusNotFound, "system.notFound")
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

// Paging of the group list.
const (
	defaultGroupPageSize = 50
	maxGroupPageSize     = 500
	maxGroupPage         = 1000000 // bounds the offset
)

// handleListGroups answers one page (page, per_page) of the groups of a
// mailbox matching the filters, in the requested order (sort), with the
// number of matching groups and, except for the excluded scope, the count
// per state.
func (c *core) handleListGroups(w http.ResponseWriter, r *http.Request) {
	mb, ok := c.mailboxFromPath(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	filter := models.GroupFilter{State: q.Get("state"), Responsible: q.Get("responsible"), Category: q.Get("category"),
		Query: q.Get("q"), Sort: q.Get("sort")}
	if !models.ValidGroupSort(filter.Sort) {
		writeError(w, http.StatusBadRequest, "system.invalidRequest")
		return
	}
	page, perPage := 1, defaultGroupPageSize
	for _, p := range []struct {
		name string
		dst  *int
		max  int
	}{{"page", &page, maxGroupPage}, {"per_page", &perPage, maxGroupPageSize}} {
		s := q.Get(p.name)
		if s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > p.max {
			writeError(w, http.StatusBadRequest, "system.invalidRequest")
			return
		}
		*p.dst = n
	}
	scope, ok := modules.GroupListScope(q.Get("scope"))
	if !ok {
		writeError(w, http.StatusBadRequest, "system.invalidRequest")
		return
	}
	filter.Scope = scope
	if filter.Category != "" && !modules.IsKnownCategory(filter.Category) {
		writeError(w, http.StatusBadRequest, "system.invalidRequest")
		return
	}
	switch filter.State {
	case "", modules.GroupStateOpen, modules.GroupStateResolved, modules.GroupStateIgnored:
	default:
		writeError(w, http.StatusBadRequest, "system.invalidRequest")
		return
	}
	switch filter.Responsible {
	case "", modules.ResponsibleSender, modules.ResponsibleRecipient, modules.ResponsibleDomain, modules.ResponsibleUnknown:
	default:
		writeError(w, http.StatusBadRequest, "system.invalidRequest")
		return
	}
	idx, err := c.openIndex(r, mb)
	if err != nil {
		writeInternalError(w, "failed to open the mail index", err)
		return
	}
	defer idx.Close()
	groups, total, err := models.ListGroupsPage(idx, filter, (page-1)*perPage, perPage)
	if err != nil {
		writeInternalError(w, "failed to list groups", err)
		return
	}
	out, err := groupDTOs(idx, groups)
	if err != nil {
		writeInternalError(w, "failed to load reports", err)
		return
	}
	resp := map[string]any{"groups": out, "total": total, "page": page, "per_page": perPage}
	// Excluded groups have no states to track, so their list carries no counts.
	if scope != models.GroupScopeExcluded {
		counts, err := models.CountGroups(idx, scope)
		if err != nil {
			writeInternalError(w, "failed to count groups", err)
			return
		}
		resp["counts"] = counts
	}
	writeJSON(w, http.StatusOK, resp)
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
		writeError(w, http.StatusBadRequest, "system.invalidRequest")
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

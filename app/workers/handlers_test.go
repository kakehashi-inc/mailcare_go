package workers

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules"
	"mailcare/app/modules/mailengine"
)

// seededCore is a test core with an admin, a user and one mailbox whose
// index was rebuilt from three sample messages (two bounces, one ordinary
// mail, no HTML part).
type seededCore struct {
	*core
	h           http.Handler
	admin, user *http.Cookie
	mb          *models.Mailbox
	keys        []string // message keys in seeding order
	reindex     *mailengine.ReindexResult
}

func newSeededCore(t *testing.T) *seededCore {
	t.Helper()
	c := newTestCore(t)
	h := c.webHandler()
	if _, err := modules.CreateUser(c.db, "admin", "", "password123", modules.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := modules.CreateUser(c.db, "bob", "", "password123", modules.RoleUser); err != nil {
		t.Fatal(err)
	}
	mb, err := modules.CreateMailbox(c.db, c.key, &modules.MailboxInput{Address: "ops@example.test",
		ImapHost: "imap.example.test", ImapUsername: "u", ImapPassword: "p"})
	if err != nil {
		t.Fatal(err)
	}
	dir := mailengine.MailboxDir(c.mailsRoot, mb.Address)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for i, name := range []string{"postfix_dsn.eml", "exim_bounce.eml", "normal.eml"} {
		raw, err := os.ReadFile(filepath.Join("..", "modules", "mailengine", "testdata", name))
		if err != nil {
			t.Fatalf("read sample: %v", err)
		}
		key := mailengine.MessageKey(time.Date(2025, 9, 1+i, 0, 0, 0, 0, time.UTC), "INBOX", 1, uint32(i+1))
		if err := os.WriteFile(filepath.Join(dir, key+".eml"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	res, err := mailengine.Reindex(context.Background(), c.mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if res.Messages != 3 || res.Bounces != 2 || res.Groups != 2 {
		t.Fatalf("unexpected seed result %+v", res)
	}
	return &seededCore{core: c, h: h, admin: login(t, h, "admin", "password123"), user: login(t, h, "bob", "password123"),
		mb: mb, keys: keys, reindex: res}
}

func (s *seededCore) path(suffix string) string {
	return "/api/v1/mailboxes/" + itoa(s.mb.ID) + suffix
}

// decode unmarshals a JSON body into v.
func decode(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

type groupsResponse struct {
	Groups        []GroupDTO         `json:"groups"`
	Counts        models.GroupCounts `json:"counts"`
	ExcludedCount int                `json:"excluded_count"`
}

// seededGroupCounts returns how many of the seeded groups are actionable and
// how many are excluded (recipient-side); the split depends on the category
// rules of mailengine, so the tests derive their expectations from it.
func seededGroupCounts(t *testing.T, s *seededCore) (all groupsResponse, actionable, excluded int) {
	t.Helper()
	rec := do(t, s.h, http.MethodGet, s.path("/groups?scope=all"), nil, s.user)
	if rec.Code != http.StatusOK {
		t.Fatalf("list all groups: %d %s", rec.Code, rec.Body.String())
	}
	decode(t, rec.Body.Bytes(), &all)
	if len(all.Groups) != s.reindex.Groups {
		t.Fatalf("scope=all lists %d groups, reindex made %d", len(all.Groups), s.reindex.Groups)
	}
	for _, g := range all.Groups {
		if g.Actionable {
			actionable++
		} else {
			excluded++
		}
	}
	return all, actionable, excluded
}

func TestGroupsEndpoints(t *testing.T) {
	s := newSeededCore(t)
	all, actionable, excluded := seededGroupCounts(t, s)
	if all.Counts.Open != len(all.Groups) || all.Counts.Resolved != 0 || all.ExcludedCount != excluded {
		t.Fatalf("scope=all counts %+v excluded %d (want %d)", all.Counts, all.ExcludedCount, excluded)
	}
	for _, g := range all.Groups {
		if g.State != "open" || g.MessageCount != 1 || g.ReportStatus != "" || g.ReportSummary != "" || !g.NeedsAnalysis {
			t.Errorf("unexpected group %+v", g)
		}
		if g.RecipientDomain == "" || g.LastSeen == nil || g.Category == "" || g.UnitValue == "" {
			t.Errorf("group lacks fields %+v", g)
		}
	}
	// The default scope is the actionable groups; excluded lists the rest.
	rec := do(t, s.h, http.MethodGet, s.path("/groups"), nil, s.user)
	if rec.Code != http.StatusOK {
		t.Fatalf("list groups: %d %s", rec.Code, rec.Body.String())
	}
	var list groupsResponse
	decode(t, rec.Body.Bytes(), &list)
	if len(list.Groups) != actionable || list.Counts.Open != actionable || list.ExcludedCount != excluded {
		t.Errorf("default scope: %d groups, counts %+v, excluded %d (want %d / %d)", len(list.Groups), list.Counts, list.ExcludedCount, actionable, excluded)
	}
	for _, g := range list.Groups {
		if !g.Actionable {
			t.Errorf("excluded group in the default scope: %+v", g)
		}
	}
	rec = do(t, s.h, http.MethodGet, s.path("/groups?scope=excluded"), nil, s.user)
	decode(t, rec.Body.Bytes(), &list)
	if len(list.Groups) != excluded || list.Counts.Open != excluded {
		t.Errorf("scope=excluded: %d groups, counts %+v (want %d)", len(list.Groups), list.Counts, excluded)
	}
	// Category filter and validation of scope / category.
	rec = do(t, s.h, http.MethodGet, s.path("/groups?scope=all&category="+all.Groups[0].Category), nil, s.user)
	decode(t, rec.Body.Bytes(), &list)
	if len(list.Groups) == 0 {
		t.Errorf("category filter %s: no groups", all.Groups[0].Category)
	}
	for _, g := range list.Groups {
		if g.Category != all.Groups[0].Category {
			t.Errorf("category filter: %+v", g)
		}
	}
	for _, bad := range []string{"?scope=bogus", "?category=bogus"} {
		if rec := do(t, s.h, http.MethodGet, s.path("/groups"+bad), nil, s.user); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, rec.Code)
		}
	}
	// Filters: state, q, and validation of both.
	rec = do(t, s.h, http.MethodGet, s.path("/groups?scope=all&state=resolved"), nil, s.user)
	decode(t, rec.Body.Bytes(), &list)
	if len(list.Groups) != 0 || list.Counts.Open != len(all.Groups) {
		t.Errorf("state filter: %d groups, counts %+v", len(list.Groups), list.Counts)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/groups?state=bogus"), nil, s.user)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid state: %d", rec.Code)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/groups?responsible=nobody"), nil, s.user)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid responsible: %d", rec.Code)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/groups?scope=all&q=nowhere.example.org"), nil, s.user)
	decode(t, rec.Body.Bytes(), &list)
	if len(list.Groups) != 1 || list.Groups[0].RecipientDomain != "nowhere.example.org" {
		t.Errorf("q filter: %+v", list.Groups)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/groups?scope=all&q=no-such-domain"), nil, s.user)
	decode(t, rec.Body.Bytes(), &list)
	if len(list.Groups) != 0 {
		t.Errorf("q filter miss: %+v", list.Groups)
	}
	// Detail.
	rec = do(t, s.h, http.MethodGet, s.path("/groups?scope=all&q=nowhere.example.org"), nil, s.user)
	decode(t, rec.Body.Bytes(), &list)
	gk := list.Groups[0].GroupKey
	rec = do(t, s.h, http.MethodGet, s.path("/groups/"+gk), nil, s.user)
	if rec.Code != http.StatusOK {
		t.Fatalf("group detail: %d %s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Group    GroupDTO                `json:"group"`
		Stats    models.GroupBounceStats `json:"stats"`
		Report   *ReportDTO              `json:"report"`
		Reports  []ReportDTO             `json:"reports"`
		Messages []MessageDTO            `json:"messages"`
	}
	decode(t, rec.Body.Bytes(), &detail)
	if detail.Group.GroupKey != gk || detail.Report != nil || len(detail.Reports) != 0 {
		t.Errorf("detail group/report: %+v %+v", detail.Group, detail.Report)
	}
	if !strings.Contains(rec.Body.String(), `"report":null`) {
		t.Errorf("report should be null: %s", rec.Body.String())
	}
	if len(detail.Stats.Recipients) != 1 || detail.Stats.Recipients[0] != "jiro@nowhere.example.org" {
		t.Errorf("stats %+v", detail.Stats)
	}
	if len(detail.Messages) != 1 || !detail.Messages[0].IsBounce || detail.Messages[0].GroupKey != gk {
		t.Errorf("messages %+v", detail.Messages)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/groups/0000000000000000"), nil, s.user)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown group: %d", rec.Code)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/groups/not-a-key"), nil, s.user)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed group key: %d", rec.Code)
	}

	// State changes (administrators only; the user role is read-only).
	if rec := do(t, s.h, http.MethodPut, s.path("/groups/"+gk+"/state"), map[string]string{"state": "resolved"}, s.user); rec.Code != http.StatusForbidden {
		t.Errorf("set state as user: %d, want 403", rec.Code)
	}
	rec = do(t, s.h, http.MethodPut, s.path("/groups/"+gk+"/state"), map[string]string{"state": "done"}, s.admin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid state: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s.h, http.MethodPut, s.path("/groups/"+gk+"/state"), map[string]string{"state": "resolved"}, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("set state: %d %s", rec.Code, rec.Body.String())
	}
	var changed struct {
		Group GroupDTO `json:"group"`
	}
	decode(t, rec.Body.Bytes(), &changed)
	if changed.Group.State != "resolved" || changed.Group.StateUpdatedAt == nil {
		t.Errorf("state response %+v", changed.Group)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/groups?scope=all"), nil, s.user)
	decode(t, rec.Body.Bytes(), &list)
	if list.Counts.Open != len(all.Groups)-1 || list.Counts.Resolved != 1 {
		t.Errorf("counts after resolve %+v", list.Counts)
	}
	if list.Groups[0].State != "open" || list.Groups[len(list.Groups)-1].State != "resolved" {
		t.Errorf("open groups should sort first: %s / %s", list.Groups[0].State, list.Groups[len(list.Groups)-1].State)
	}

	// Analyze queues a job for that group (administrators only).
	if rec := do(t, s.h, http.MethodPost, s.path("/groups/"+gk+"/analyze"), nil, s.user); rec.Code != http.StatusForbidden {
		t.Errorf("analyze as user: %d, want 403", rec.Code)
	}
	rec = do(t, s.h, http.MethodPost, s.path("/groups/"+gk+"/analyze"), nil, s.admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"target":"`+gk+`"`) {
		t.Errorf("analyze: %d %s", rec.Code, rec.Body.String())
	}
}

type messagesResponse struct {
	Messages []MessageDTO `json:"messages"`
	Total    int          `json:"total"`
	Page     int          `json:"page"`
	PerPage  int          `json:"per_page"`
}

func TestMessagesEndpoints(t *testing.T) {
	s := newSeededCore(t)

	rec := do(t, s.h, http.MethodGet, s.path("/messages"), nil, s.user)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var list messagesResponse
	decode(t, rec.Body.Bytes(), &list)
	if list.Total != 3 || len(list.Messages) != 3 || list.Page != 1 || list.PerPage != defaultPerPage {
		t.Errorf("list %+v", list)
	}
	// Newest first.
	if list.Messages[0].MessageKey != s.keys[2] {
		t.Errorf("order: first %s, want %s", list.Messages[0].MessageKey, s.keys[2])
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages?page=2&per_page=2"), nil, s.user)
	decode(t, rec.Body.Bytes(), &list)
	if list.Total != 3 || len(list.Messages) != 1 || list.Page != 2 || list.PerPage != 2 {
		t.Errorf("page 2: %+v", list)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages?only_bounce=1"), nil, s.user)
	decode(t, rec.Body.Bytes(), &list)
	if list.Total != 2 || len(list.Messages) != 2 {
		t.Errorf("only_bounce: %+v", list)
	}
	for _, m := range list.Messages {
		if !m.IsBounce || m.GroupKey == "" {
			t.Errorf("non-bounce in only_bounce: %+v", m)
		}
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages?q=campaign"), nil, s.user)
	decode(t, rec.Body.Bytes(), &list)
	if list.Total != 1 || list.Messages[0].IsBounce {
		t.Errorf("q: %+v", list)
	}
	for _, bad := range []string{"?page=0", "?per_page=0", "?per_page=1000", "?page=x"} {
		if rec := do(t, s.h, http.MethodGet, s.path("/messages"+bad), nil, s.user); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, rec.Code)
		}
	}

	// Detail of a bounce.
	rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[1]), nil, s.user)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Message MessageDTO     `json:"message"`
		Bounce  *BounceDTO     `json:"bounce"`
		Text    string         `json:"text"`
		HasHTML bool           `json:"has_html"`
		Headers map[string]any `json:"headers"`
	}
	decode(t, rec.Body.Bytes(), &detail)
	if detail.Message.MessageKey != s.keys[1] || detail.Bounce == nil || detail.Bounce.OriginalRecipient != "jiro@nowhere.example.org" {
		t.Errorf("detail %+v bounce %+v", detail.Message, detail.Bounce)
	}
	if !strings.Contains(detail.Text, "could not be delivered") || detail.HasHTML || len(detail.Headers) == 0 {
		t.Errorf("text/html/headers: %q %v %d", detail.Text[:min(len(detail.Text), 40)], detail.HasHTML, len(detail.Headers))
	}
	// An ordinary mail has bounce null.
	rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[2]), nil, s.user)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"bounce":null`) {
		t.Errorf("normal mail detail: %d %s", rec.Code, rec.Body.String())
	}
	// Bad keys.
	rec = do(t, s.h, http.MethodGet, s.path("/messages/20250101-000000_000000000000"), nil, s.user)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown key: %d", rec.Code)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages/..%2F..%2Fmailcare.key"), nil, s.user)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("traversal key: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages/..%2F..%2Fmailcare.key/raw"), nil, s.user)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("traversal raw: %d", rec.Code)
	}

	// Raw download.
	rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[0]+"/raw"), nil, s.user)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "message/rfc822" ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), `attachment; filename="`+s.keys[0]+`.eml"`) {
		t.Errorf("raw: %d %v", rec.Code, rec.Header())
	}
	if !strings.HasPrefix(rec.Body.String(), "Return-Path:") && !strings.Contains(rec.Body.String(), "Subject:") {
		t.Errorf("raw body does not look like the .eml")
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages/20250101-000000_000000000000/raw"), nil, s.user)
	if rec.Code != http.StatusNotFound {
		t.Errorf("raw unknown: %d", rec.Code)
	}

	// HTML: none of the samples has an HTML part.
	rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[0]+"/html"), nil, s.user)
	if rec.Code != http.StatusNotFound {
		t.Errorf("html absent: %d", rec.Code)
	}
	// With an HTML file present it is sanitized and served with the CSP.
	htmlPath := mailengine.MessageFilePath(s.mailsRoot, s.mb.Address, s.keys[0], "html")
	if err := os.WriteFile(htmlPath, []byte(`<p onclick="x()">hi</p><script>alert(1)</script><a href="javascript:1">l</a>`), 0o600); err != nil {
		t.Fatal(err)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[0]+"/html"), nil, s.user)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Security-Policy") != htmlCSP ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("html: %d %v", rec.Code, rec.Header())
	}
	if body := rec.Body.String(); strings.Contains(body, "script") || strings.Contains(body, "onclick") || strings.Contains(body, "javascript:") || !strings.Contains(body, "<p>hi</p>") {
		t.Errorf("html not sanitized: %s", body)
	}
	// Everything needs a session.
	rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[0]+"/raw"), nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("raw without session: %d", rec.Code)
	}
}

func TestJobsEndpoints(t *testing.T) {
	s := newSeededCore(t)
	// Only administrators queue jobs (the user role is read-only).
	for _, kind := range []string{"reindex", "reclassify", "fetch", "group", "analyze", "sync"} {
		rec := do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": kind, "mailbox_id": s.mb.ID}, s.user)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s as user: %d", kind, rec.Code)
		}
	}
	for _, kind := range []string{"fetch", "group", "analyze"} {
		rec := do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": kind, "mailbox_id": s.mb.ID}, s.admin)
		if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"kind":"`+kind+`"`) {
			t.Errorf("%s as admin: %d %s", kind, rec.Code, rec.Body.String())
		}
	}
	rec := do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "analyze", "target": "*"}, s.admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"mailbox_id":null`) {
		t.Errorf("all-mailbox analyze: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "sync", "mailbox_id": s.mb.ID}, s.admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("sync: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Job     JobDTO `json:"job"`
		Created bool   `json:"created"`
	}
	decode(t, rec.Body.Bytes(), &env)
	if !env.Created || env.Job.Status != "queued" || env.Job.MailboxAddress != s.mb.Address || env.Job.RequestedBy != "web:admin" {
		t.Errorf("job %+v created %v", env.Job, env.Created)
	}
	rec = do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "sync", "mailbox_id": s.mb.ID}, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate sync: %d %s", rec.Code, rec.Body.String())
	}
	var dup struct {
		Job     JobDTO `json:"job"`
		Created bool   `json:"created"`
	}
	decode(t, rec.Body.Bytes(), &dup)
	if dup.Created || dup.Job.ID != env.Job.ID {
		t.Errorf("duplicate: %+v created %v", dup.Job, dup.Created)
	}
	for _, bad := range []map[string]any{{"kind": "bogus"}, {"kind": "check"}, {"kind": "analyze", "target": "0123456789abcdef"},
		{"kind": "sync", "mailbox_id": 999}, {"kind": "sync", "mailbox_id": -1}} {
		if rec := do(t, s.h, http.MethodPost, "/api/v1/jobs", bad, s.admin); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: %d, want 400", bad, rec.Code)
		}
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/jobs?limit=1", nil, s.user)
	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), `"kind":`) != 1 {
		t.Errorf("list limit: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, s.h, http.MethodGet, "/api/v1/jobs?limit=0", nil, s.user); rec.Code != http.StatusBadRequest {
		t.Errorf("limit 0: %d", rec.Code)
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/jobs/"+itoa(env.Job.ID), nil, s.user)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":`+itoa(env.Job.ID)) {
		t.Errorf("get job: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, s.h, http.MethodGet, "/api/v1/jobs/999", nil, s.user); rec.Code != http.StatusNotFound {
		t.Errorf("unknown job: %d", rec.Code)
	}
	// Cancel: user forbidden, admin cancels a queued job, a finished one is 409.
	if rec := do(t, s.h, http.MethodDelete, "/api/v1/jobs/"+itoa(env.Job.ID), nil, s.user); rec.Code != http.StatusForbidden {
		t.Errorf("cancel as user: %d", rec.Code)
	}
	rec = do(t, s.h, http.MethodDelete, "/api/v1/jobs/"+itoa(env.Job.ID), nil, s.admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"canceled"`) {
		t.Errorf("cancel queued: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, s.h, http.MethodDelete, "/api/v1/jobs/"+itoa(env.Job.ID), nil, s.admin); rec.Code != http.StatusConflict {
		t.Errorf("cancel canceled: %d", rec.Code)
	}
	rec = do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "reclassify"}, s.admin)
	decode(t, rec.Body.Bytes(), &env)
	if err := models.FinishJob(s.db, env.Job.ID, "done", ""); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, s.h, http.MethodDelete, "/api/v1/jobs/"+itoa(env.Job.ID), nil, s.admin); rec.Code != http.StatusConflict {
		t.Errorf("cancel done: %d", rec.Code)
	}
}

func TestSettingsValidationAndPersistence(t *testing.T) {
	s := newSeededCore(t)
	for _, bad := range []map[string]any{
		{"check_times": []string{"25:00"}},
		{"check_times": []string{"6:5"}},
		{"agent_provider": "no-such-provider"},
		{"workers": 0},
		{"workers": modules.MaxWorkers + 1},
	} {
		rec := do(t, s.h, http.MethodPut, "/api/v1/settings", bad, s.admin)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%v: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	rec := do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"check_times": []string{"7:30", "07:30", "23:00"}, "agent_enabled": false}, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	var st struct {
		CheckTimes    []string `json:"check_times"`
		AgentProvider string   `json:"agent_provider"`
		AgentEnabled  bool     `json:"agent_enabled"`
		Workers       int      `json:"workers"`
		WebPort       int      `json:"web_port"`
		DataDir       string   `json:"data_dir"`
	}
	decode(t, rec.Body.Bytes(), &st)
	if strings.Join(st.CheckTimes, ",") != "07:30,23:00" || st.AgentEnabled || st.AgentProvider != modules.DefaultAgentProvider ||
		st.WebPort != modules.DefaultWebPort || st.DataDir != s.dataDir || st.Workers != modules.DefaultWorkers {
		t.Errorf("settings %+v", st)
	}
	// The worker count is persisted and applied to the job manager at once.
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"workers": 5}, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("workers: %d %s", rec.Code, rec.Body.String())
	}
	decode(t, rec.Body.Bytes(), &st)
	if st.Workers != 5 || s.jm.Workers() != 5 || models.GetSetting(s.db, modules.SettingWorkers) != "5" {
		t.Errorf("workers not applied: dto %d manager %d stored %q", st.Workers, s.jm.Workers(), models.GetSetting(s.db, modules.SettingWorkers))
	}
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"workers": modules.DefaultWorkers}, s.admin)
	if rec.Code != http.StatusOK || s.jm.Workers() != modules.DefaultWorkers {
		t.Errorf("workers back to default: %d manager %d", rec.Code, s.jm.Workers())
	}
	if _, found, _ := models.GetSettingStrict(s.db, modules.SettingWorkers); found {
		t.Errorf("default workers still stored")
	}
	if got := models.GetSetting(s.db, modules.SettingCheckTimes); got != "07:30,23:00" {
		t.Errorf("check_times persisted as %q", got)
	}
	if got := models.GetSetting(s.db, modules.SettingAgentEnabled); got != "0" {
		t.Errorf("agent_enabled persisted as %q", got)
	}
	// Restoring the default deletes the row; a partial update leaves the rest alone.
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"check_times": strings.Split(modules.DefaultCheckTimes, ",")}, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if _, found, _ := models.GetSettingStrict(s.db, modules.SettingCheckTimes); found {
		t.Errorf("default check_times still stored")
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/settings", nil, s.user)
	decode(t, rec.Body.Bytes(), &st)
	if st.AgentEnabled || strings.Join(st.CheckTimes, ",") != modules.DefaultCheckTimes {
		t.Errorf("after partial update %+v", st)
	}
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"check_times": []string{}}, s.admin)
	decode(t, rec.Body.Bytes(), &st)
	if len(st.CheckTimes) != 0 || !strings.Contains(rec.Body.String(), `"check_times":[]`) {
		t.Errorf("empty check_times: %s", rec.Body.String())
	}
}

func TestDashboardAndMailboxStats(t *testing.T) {
	s := newSeededCore(t)
	_, actionable, excluded := seededGroupCounts(t, s)
	rec := do(t, s.h, http.MethodGet, "/api/v1/dashboard", nil, s.user)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard: %d %s", rec.Code, rec.Body.String())
	}
	var d DashboardDTO
	decode(t, rec.Body.Bytes(), &d)
	if d.Totals.Mailboxes != 1 || d.Totals.Messages != 3 || d.Totals.Bounces != 2 || d.Totals.OpenGroups != actionable || d.Totals.Unclassified != 0 {
		t.Errorf("totals %+v (actionable %d)", d.Totals, actionable)
	}
	if len(d.Mailboxes) != 1 || d.Mailboxes[0].Stats == nil || d.Mailboxes[0].Stats.Messages != 3 ||
		d.Mailboxes[0].Stats.Groups.Open != actionable || d.Mailboxes[0].Stats.ExcludedGroups != excluded {
		t.Errorf("mailboxes %+v (actionable %d, excluded %d)", d.Mailboxes, actionable, excluded)
	}
	if len(d.RecentGroups) != actionable {
		t.Errorf("recent groups %+v (want %d actionable)", d.RecentGroups, actionable)
	}
	for i, g := range d.RecentGroups {
		if !g.Actionable || g.MailboxAddress != s.mb.Address || g.MailboxID != s.mb.ID || g.LastSeen == nil {
			t.Errorf("recent group %+v", g)
		}
		if i > 0 && *d.RecentGroups[i-1].LastSeen < *g.LastSeen {
			t.Errorf("recent groups not newest first: %+v", d.RecentGroups)
		}
	}
	if d.NextCheckAt == nil || len(d.CheckTimes) != 3 || d.Agent.Provider != modules.DefaultAgentProvider || !d.Agent.Enabled {
		t.Errorf("schedule/agent %+v %v %+v", d.CheckTimes, d.NextCheckAt, d.Agent)
	}
	if len(d.ActiveJobs) != 0 || len(d.RecentJobs) != 0 {
		t.Errorf("jobs should be empty: %+v %+v", d.ActiveJobs, d.RecentJobs)
	}
	// A second mailbox without any data contributes zeros, not an error.
	if _, err := modules.CreateMailbox(s.db, s.key, &modules.MailboxInput{Address: "empty@example.test",
		ImapHost: "h", ImapUsername: "u", ImapPassword: "p"}); err != nil {
		t.Fatal(err)
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/dashboard", nil, s.user)
	decode(t, rec.Body.Bytes(), &d)
	if d.Totals.Mailboxes != 2 || d.Totals.Messages != 3 || len(d.Mailboxes) != 2 {
		t.Errorf("with an empty mailbox %+v", d.Totals)
	}

	// Mailbox list: stats only on request.
	rec = do(t, s.h, http.MethodGet, "/api/v1/mailboxes", nil, s.user)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"stats"`) {
		t.Errorf("list without stats: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/mailboxes?stats=1", nil, s.user)
	var listed struct {
		Mailboxes []MailboxDTO `json:"mailboxes"`
	}
	decode(t, rec.Body.Bytes(), &listed)
	if rec.Code != http.StatusOK || len(listed.Mailboxes) != 2 || listed.Mailboxes[0].Stats == nil {
		t.Fatalf("list with stats: %d %s", rec.Code, rec.Body.String())
	}
	for _, mb := range listed.Mailboxes {
		if mb.Address != s.mb.Address {
			continue
		}
		st := mb.Stats
		if st.Messages != 3 || st.Bounces != 2 || st.Unclassified != 0 || st.Groups.Open != actionable || st.ExcludedGroups != excluded {
			t.Errorf("stats %+v (actionable %d, excluded %d)", st, actionable, excluded)
		}
	}
	if !strings.Contains(rec.Body.String(), `"unclassified":0`) || !strings.Contains(rec.Body.String(), `"excluded_groups":`) {
		t.Errorf("stats fields missing: %s", rec.Body.String())
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/mailboxes/"+itoa(s.mb.ID), nil, s.user)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"stats":{"messages":3`) {
		t.Errorf("get mailbox: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, s.h, http.MethodGet, "/api/v1/mailboxes/999", nil, s.user); rec.Code != http.StatusNotFound {
		t.Errorf("unknown mailbox: %d", rec.Code)
	}
}

package workers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	createUser(t, c.db, "admin", "", "password123", modules.RoleAdmin)
	createUser(t, c.db, "bob", "", "password123", modules.RoleUser)
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
		path := mailengine.MessageFilePath(c.mailsRoot, mb.Address, key, "eml")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
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
	Groups []GroupDTO         `json:"groups"`
	Counts models.GroupCounts `json:"counts"`
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
	if all.Counts.Open != len(all.Groups) || all.Counts.Resolved != 0 || excluded == 0 {
		t.Fatalf("scope=all counts %+v, %d excluded groups (want some)", all.Counts, excluded)
	}
	for _, g := range all.Groups {
		// Only actionable groups are flagged for analysis (recipient-side
		// groups are never analyzed automatically).
		if g.State != "open" || g.MessageCount != 1 || g.ReportStatus != "" || g.ReportSummary != "" || g.NeedsAnalysis != g.Actionable {
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
	if len(list.Groups) != actionable || list.Counts.Open != actionable || strings.Contains(rec.Body.String(), `"excluded_count"`) {
		t.Errorf("default scope: %d groups, counts %+v (want %d): %s", len(list.Groups), list.Counts, actionable, rec.Body.String())
	}
	for _, g := range list.Groups {
		if !g.Actionable {
			t.Errorf("excluded group in the default scope: %+v", g)
		}
	}
	// The excluded list has no states to track, so it carries no counts.
	rec = do(t, s.h, http.MethodGet, s.path("/groups?scope=excluded"), nil, s.user)
	list = groupsResponse{}
	decode(t, rec.Body.Bytes(), &list)
	if len(list.Groups) != excluded || strings.Contains(rec.Body.String(), `"counts"`) {
		t.Errorf("scope=excluded: %d groups (want %d): %s", len(list.Groups), excluded, rec.Body.String())
	}
	// A handled excluded group moves to the actionable list; reopening it
	// sends it back.
	moved := list.Groups[0].GroupKey
	for _, st := range []string{"resolved", "ignored"} {
		if rec := do(t, s.h, http.MethodPut, s.path("/groups/"+moved+"/state"), map[string]string{"state": st}, s.admin); rec.Code != http.StatusOK {
			t.Fatalf("set excluded group %s: %d %s", st, rec.Code, rec.Body.String())
		}
		rec = do(t, s.h, http.MethodGet, s.path("/groups?scope=excluded"), nil, s.user)
		decode(t, rec.Body.Bytes(), &list)
		if len(list.Groups) != excluded-1 {
			t.Errorf("%s: excluded list has %d groups, want %d", st, len(list.Groups), excluded-1)
		}
		rec = do(t, s.h, http.MethodGet, s.path("/groups?state="+st), nil, s.user)
		decode(t, rec.Body.Bytes(), &list)
		if len(list.Groups) != 1 || list.Groups[0].GroupKey != moved || list.Groups[0].Actionable ||
			list.Counts.Open != actionable || (st == "resolved" && list.Counts.Resolved != 1) || (st == "ignored" && list.Counts.Ignored != 1) {
			t.Errorf("%s: actionable list %+v counts %+v", st, list.Groups, list.Counts)
		}
	}
	if rec := do(t, s.h, http.MethodPut, s.path("/groups/"+moved+"/state"), map[string]string{"state": "open"}, s.admin); rec.Code != http.StatusOK {
		t.Fatalf("reopen excluded group: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s.h, http.MethodGet, s.path("/groups?scope=excluded"), nil, s.user)
	decode(t, rec.Body.Bytes(), &list)
	if len(list.Groups) != excluded {
		t.Errorf("after reopen: excluded list has %d groups, want %d", len(list.Groups), excluded)
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

	// State changes (every signed-in user).
	if rec := do(t, s.h, http.MethodPut, s.path("/groups/"+gk+"/state"), map[string]string{"state": "ignored"}, s.user); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"state":"ignored"`) {
		t.Errorf("set state as user: %d %s, want 200", rec.Code, rec.Body.String())
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
	Messages []MessageDTO             `json:"messages"`
	Total    int                      `json:"total"`
	Page     int                      `json:"page"`
	PerPage  int                      `json:"per_page"`
	Counts   models.MessageKindCounts `json:"counts"`
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
	// Every list carries the counts per kind for the same search.
	if list.Counts.All != 3 || list.Counts.Bounce != 2 || list.Counts.Other != 1 {
		t.Errorf("counts: %+v", list.Counts)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages?kind=bounce"), nil, s.user)
	decode(t, rec.Body.Bytes(), &list)
	if list.Total != 2 || len(list.Messages) != 2 || list.Counts.All != 3 {
		t.Errorf("kind=bounce: %+v", list)
	}
	for _, m := range list.Messages {
		if !m.IsBounce || m.GroupKey == "" {
			t.Errorf("non-bounce in kind=bounce: %+v", m)
		}
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages?kind=other"), nil, s.user)
	decode(t, rec.Body.Bytes(), &list)
	if list.Total != 1 || len(list.Messages) != 1 || list.Messages[0].IsBounce {
		t.Errorf("kind=other: %+v", list)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages?kind=all"), nil, s.user)
	decode(t, rec.Body.Bytes(), &list)
	if list.Total != 3 {
		t.Errorf("kind=all: %+v", list)
	}
	if rec = do(t, s.h, http.MethodGet, s.path("/messages?kind=nope"), nil, s.user); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown kind: %d", rec.Code)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages?q=campaign"), nil, s.user)
	decode(t, rec.Body.Bytes(), &list)
	if list.Total != 1 || list.Messages[0].IsBounce || list.Counts.All != 1 || list.Counts.Other != 1 || list.Counts.Bounce != 0 {
		t.Errorf("q: %+v", list)
	}
	for _, bad := range []string{"?page=0", "?per_page=0", "?per_page=1000", "?page=x"} {
		if rec := do(t, s.h, http.MethodGet, s.path("/messages"+bad), nil, s.user); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, rec.Code)
		}
	}

	// Detail of a bounce: the text sections, the decoded headers and the
	// bounce details with the responsible party of its group.
	rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[1]), nil, s.user)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Message      MessageDTO        `json:"message"`
		Bounce       *BounceDTO        `json:"bounce"`
		TextSections []string          `json:"text_sections"`
		Headers      map[string]string `json:"headers"`
	}
	decode(t, rec.Body.Bytes(), &detail)
	if detail.Message.MessageKey != s.keys[1] || detail.Bounce == nil || detail.Bounce.OriginalRecipient != "jiro@nowhere.example.org" {
		t.Errorf("detail %+v bounce %+v", detail.Message, detail.Bounce)
	}
	if detail.Message.TextCount < 1 || len(detail.TextSections) != detail.Message.TextCount || detail.Message.HTMLCount != 0 ||
		!strings.Contains(strings.Join(detail.TextSections, "\n"), "could not be delivered") {
		t.Errorf("text sections: count %d, %d sections %q", detail.Message.TextCount, len(detail.TextSections), detail.TextSections)
	}
	if detail.Headers["Subject"] == "" || detail.Headers["From"] == "" {
		t.Errorf("headers must be decoded from the .eml: %v", detail.Headers)
	}
	if detail.Message.Rule == "" || !detail.Message.IsBounce || detail.Message.GroupKey == "" {
		t.Errorf("detection outcome: %+v", detail.Message)
	}
	// The responsible party comes from the group of the bounce.
	idx, err := s.openIndex(httptest.NewRequest(http.MethodGet, "/", nil), s.mb)
	if err != nil {
		t.Fatal(err)
	}
	if err := models.UpdateGroupResponsible(idx, detail.Message.GroupKey, modules.ResponsibleDomain); err != nil {
		t.Fatal(err)
	}
	idx.Close()
	rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[1]), nil, s.user)
	decode(t, rec.Body.Bytes(), &detail)
	if detail.Bounce == nil || detail.Bounce.Responsible != modules.ResponsibleDomain {
		t.Errorf("bounce responsible: %+v", detail.Bounce)
	}
	// An ordinary mail has bounce null and an empty text list when it has
	// no text section.
	rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[2]), nil, s.user)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"bounce":null`) || !strings.Contains(rec.Body.String(), `"text_sections":[`) {
		t.Errorf("normal mail detail: %d %s", rec.Code, rec.Body.String())
	}
	// Bad keys.
	rec = do(t, s.h, http.MethodGet, s.path("/messages/20250101-000000_000000000000"), nil, s.user)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown key: %d", rec.Code)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages/..%2F..%2Fmailcare.db"), nil, s.user)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("traversal key: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages/..%2F..%2Fmailcare.db/raw"), nil, s.user)
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
	rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[0]+"/html/1"), nil, s.user)
	if rec.Code != http.StatusNotFound {
		t.Errorf("html absent: %d", rec.Code)
	}
	for _, n := range []string{"0", "-1", "x"} {
		rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[0]+"/html/"+n), nil, s.user)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("html section %q: %d", n, rec.Code)
		}
	}
	// With two HTML sections present (and counted by the index) each one is
	// served on its own, sanitized and with the CSP.
	dir := mailengine.MailboxDir(s.mailsRoot, s.mb.Address)
	for i, html := range []string{`<p onclick="x()">hi</p><script>alert(1)</script><a href="javascript:1">l</a>`, `<p>second</p>`} {
		if err := os.WriteFile(mailengine.SectionFilePath(dir, s.keys[0], "html", i+1), []byte(html), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	idx, err = s.openIndex(httptest.NewRequest(http.MethodGet, "/", nil), s.mb)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Exec(`UPDATE messages SET html_count = 2 WHERE message_key = ?`, s.keys[0]); err != nil {
		t.Fatal(err)
	}
	idx.Close()
	rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[0]+"/html/1"), nil, s.user)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Security-Policy") != htmlCSP ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("html: %d %v", rec.Code, rec.Header())
	}
	if body := rec.Body.String(); strings.Contains(body, "script") || strings.Contains(body, "onclick") || strings.Contains(body, "javascript:") ||
		!strings.Contains(body, "<p>hi</p>") || strings.Contains(body, "second") {
		t.Errorf("html section 1 not sanitized or not served alone: %s", body)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[0]+"/html/2"), nil, s.user)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<p>second</p>") || strings.Contains(rec.Body.String(), "hi") {
		t.Errorf("html section 2: %d %s", rec.Code, rec.Body.String())
	}
	// A number past html_count is not a section.
	rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[0]+"/html/3"), nil, s.user)
	if rec.Code != http.StatusNotFound {
		t.Errorf("html section past the count: %d", rec.Code)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[0]), nil, s.user)
	if !strings.Contains(rec.Body.String(), `"html_count":2`) {
		t.Errorf("html_count not reported: %s", rec.Body.String())
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
	for _, kind := range []string{"reindex", "reclassify", "fetch", "group", "analyze", "sync", "cleanup"} {
		rec := do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": kind, "mailbox_id": s.mb.ID}, s.user)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s as user: %d", kind, rec.Code)
		}
	}
	for _, kind := range []string{"fetch", "group", "analyze", "cleanup"} {
		rec := do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": kind, "mailbox_id": s.mb.ID}, s.admin)
		if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"kind":"`+kind+`"`) {
			t.Errorf("%s as admin: %d %s", kind, rec.Code, rec.Body.String())
		}
	}
	rec := do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "analyze", "target": "*"}, s.admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"mailbox_id":null`) {
		t.Errorf("all-mailbox analyze: %d %s", rec.Code, rec.Body.String())
	}
	// A cleanup without a mailbox is the expansion job the scheduler queues daily.
	rec = do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "cleanup"}, s.admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"kind":"cleanup"`) || !strings.Contains(rec.Body.String(), `"mailbox_id":null`) {
		t.Errorf("all-mailbox cleanup: %d %s", rec.Code, rec.Body.String())
	}
	var expansion struct {
		Job JobDTO `json:"job"`
	}
	decode(t, rec.Body.Bytes(), &expansion)
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
	for _, bad := range []map[string]any{{"kind": "bogus"}, {"kind": "analyze", "target": "0123456789abcdef"},
		{"kind": "sync", "mailbox_id": -1}} {
		if rec := do(t, s.h, http.MethodPost, "/api/v1/jobs", bad, s.admin); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: %d, want 400", bad, rec.Code)
		}
	}
	if rec := do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "sync", "mailbox_id": 999}, s.admin); rec.Code != http.StatusNotFound {
		t.Errorf("unknown mailbox: %d, want 404", rec.Code)
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/jobs?limit=1", nil, s.admin)
	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), `"kind":`) != 1 {
		t.Errorf("list limit: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, s.h, http.MethodGet, "/api/v1/jobs?limit=0", nil, s.admin); rec.Code != http.StatusBadRequest {
		t.Errorf("limit 0: %d", rec.Code)
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/jobs/"+itoa(env.Job.ID), nil, s.admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":`+itoa(env.Job.ID)) {
		t.Errorf("get job: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, s.h, http.MethodGet, "/api/v1/jobs/999", nil, s.admin); rec.Code != http.StatusNotFound {
		t.Errorf("unknown job: %d", rec.Code)
	}
	// Cancel: user forbidden, admin cancels a queued job, a finished one is 409.
	// Members see no jobs at all (list, detail) and cannot cancel.
	for _, path := range []string{"/api/v1/jobs", "/api/v1/jobs/" + itoa(env.Job.ID)} {
		if rec := do(t, s.h, http.MethodGet, path, nil, s.user); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s as user: %d, want 403", path, rec.Code)
		}
	}
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

	// Deleting the mailbox leaves its jobs without a mailbox; they are
	// marked mailbox_deleted (the expansion jobs are not).
	// (The fetch job queued at the top of the test is still waiting, so the
	// submission returns it.)
	rec = do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "fetch", "mailbox_id": s.mb.ID}, s.admin)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("fetch: %d %s", rec.Code, rec.Body.String())
	}
	decode(t, rec.Body.Bytes(), &env)
	if env.Job.MailboxDeleted {
		t.Errorf("a job of an existing mailbox marked deleted: %+v", env.Job)
	}
	if rec := do(t, s.h, http.MethodDelete, "/api/v1/mailboxes/"+itoa(s.mb.ID)+"?keep_data=1", nil, s.admin); rec.Code != http.StatusOK {
		t.Fatalf("delete mailbox: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/jobs/"+itoa(env.Job.ID), nil, s.admin)
	var one struct {
		Job JobDTO `json:"job"`
	}
	decode(t, rec.Body.Bytes(), &one)
	if rec.Code != http.StatusOK || one.Job.Status != "canceled" || one.Job.MailboxID != nil || one.Job.MailboxAddress != "" || !one.Job.MailboxDeleted {
		t.Errorf("job of the deleted mailbox: %d %+v", rec.Code, one.Job)
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/jobs?limit=50", nil, s.admin)
	var list struct {
		Jobs []JobDTO `json:"jobs"`
	}
	decode(t, rec.Body.Bytes(), &list)
	seen := 0
	for _, j := range list.Jobs {
		if j.ID == expansion.Job.ID {
			seen++
			if j.MailboxID != nil || j.MailboxDeleted {
				t.Errorf("the cleanup expansion job after the deletion: %+v", j)
			}
		}
	}
	if seen != 1 {
		t.Errorf("the cleanup expansion job is not listed:\n%s", rec.Body.String())
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
		{"agent_keep_days": 0},
		{"agent_keep_days": modules.MaxAgentKeepDays + 1},
		{"mail_keep_days": 0},
		{"mail_keep_days": modules.MaxMailKeepDays + 1},
		{"mail_keep_days": -1},
		{"cleanup_time": "24:00"},
		{"cleanup_time": "02:00,03:00"},
		{"cleanup_time": ""},
		{"agent_model": "-c evil"},
		{"agent_model": "gpt 5"},
		{"agent_model": strings.Repeat("a", 101)},
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
		AgentKeepDays int      `json:"agent_keep_days"`
		MailKeepDays  int      `json:"mail_keep_days"`
		CleanupTime   string   `json:"cleanup_time"`
		Workers       int      `json:"workers"`
		WebPort       int      `json:"web_port"`
		DataDir       string   `json:"data_dir"`
	}
	decode(t, rec.Body.Bytes(), &st)
	if strings.Join(st.CheckTimes, ",") != "07:30,23:00" || st.AgentEnabled || st.AgentProvider != modules.DefaultAgentProvider ||
		st.WebPort != modules.DefaultWebPort || st.DataDir != s.dataDir || st.Workers != modules.DefaultWorkers ||
		st.AgentKeepDays != modules.DefaultAgentKeepDays || st.MailKeepDays != modules.DefaultMailKeepDays ||
		st.CleanupTime != modules.DefaultCleanupTime {
		t.Errorf("settings %+v", st)
	}
	// The cleanup time is stored normalized (non-default only).
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"cleanup_time": "3:30"}, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("cleanup_time: %d %s", rec.Code, rec.Body.String())
	}
	decode(t, rec.Body.Bytes(), &st)
	if st.CleanupTime != "03:30" || models.GetSetting(s.db, modules.SettingCleanupTime) != "03:30" {
		t.Errorf("cleanup_time not applied: dto %q stored %q", st.CleanupTime, models.GetSetting(s.db, modules.SettingCleanupTime))
	}
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"cleanup_time": modules.DefaultCleanupTime}, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if _, found, _ := models.GetSettingStrict(s.db, modules.SettingCleanupTime); found {
		t.Errorf("default cleanup_time still stored")
	}
	// The mail retention is stored (non-default only) and a rejected value
	// leaves it alone.
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"mail_keep_days": 365}, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("mail_keep_days: %d %s", rec.Code, rec.Body.String())
	}
	decode(t, rec.Body.Bytes(), &st)
	if st.MailKeepDays != 365 || modules.ResolveMailKeepDays(s.db) != 365 || models.GetSetting(s.db, modules.SettingMailKeepDays) != "365" {
		t.Errorf("mail_keep_days not applied: dto %d resolved %d stored %q", st.MailKeepDays, modules.ResolveMailKeepDays(s.db), models.GetSetting(s.db, modules.SettingMailKeepDays))
	}
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"mail_keep_days": modules.MaxMailKeepDays + 1, "workers": 3}, s.admin)
	if rec.Code != http.StatusBadRequest || modules.ResolveMailKeepDays(s.db) != 365 || s.jm.Workers() != modules.DefaultWorkers {
		t.Errorf("rejected update changed something: %d, retention %d, workers %d", rec.Code, modules.ResolveMailKeepDays(s.db), s.jm.Workers())
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/settings", nil, s.user)
	decode(t, rec.Body.Bytes(), &st)
	if st.MailKeepDays != 365 {
		t.Errorf("GET mail_keep_days = %d", st.MailKeepDays)
	}
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"mail_keep_days": modules.DefaultMailKeepDays}, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if _, found, _ := models.GetSettingStrict(s.db, modules.SettingMailKeepDays); found {
		t.Errorf("default mail_keep_days still stored")
	}
	// The retention of the agent run directories is stored (non-default only).
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"agent_keep_days": 90}, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent_keep_days: %d %s", rec.Code, rec.Body.String())
	}
	decode(t, rec.Body.Bytes(), &st)
	if st.AgentKeepDays != 90 || modules.ResolveAgentKeepDays(s.db) != 90 || models.GetSetting(s.db, modules.SettingAgentKeepDays) != "90" {
		t.Errorf("agent_keep_days not applied: dto %d resolved %d stored %q", st.AgentKeepDays, modules.ResolveAgentKeepDays(s.db), models.GetSetting(s.db, modules.SettingAgentKeepDays))
	}
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"agent_keep_days": modules.DefaultAgentKeepDays}, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if _, found, _ := models.GetSettingStrict(s.db, modules.SettingAgentKeepDays); found {
		t.Errorf("default agent_keep_days still stored")
	}
	// The agent model is stored as given, "" removes it (the CLI default).
	var model struct {
		AgentModel string `json:"agent_model"`
	}
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"agent_model": " gpt-5.5 "}, s.admin)
	decode(t, rec.Body.Bytes(), &model)
	if rec.Code != http.StatusOK || model.AgentModel != "gpt-5.5" || modules.ResolveAgentModel(s.db) != "gpt-5.5" {
		t.Errorf("agent_model: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"agent_model": ""}, s.admin)
	decode(t, rec.Body.Bytes(), &model)
	if _, found, _ := models.GetSettingStrict(s.db, modules.SettingAgentModel); rec.Code != http.StatusOK || found || model.AgentModel != "" {
		t.Errorf("empty agent_model should remove the setting: %d %s", rec.Code, rec.Body.String())
	}
	// The reasoning level is checked against the model before anything is
	// saved (m1 accepts low and high).
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "models_cache.json"), []byte(
		`{"models":[{"slug":"m1","visibility":"list","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var effort struct {
		AgentModel  string `json:"agent_model"`
		AgentEffort string `json:"agent_reasoning_effort"`
	}
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"agent_model": "m1", "agent_reasoning_effort": "high"}, s.admin)
	decode(t, rec.Body.Bytes(), &effort)
	if rec.Code != http.StatusOK || effort.AgentModel != "m1" || effort.AgentEffort != "high" {
		t.Errorf("agent_reasoning_effort: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"agent_reasoning_effort": "medium", "workers": 3}, s.admin)
	if rec.Code != http.StatusBadRequest || modules.ResolveAgentReasoningEffort(s.db) != "high" || s.jm.Workers() == 3 {
		t.Errorf("a level the model does not accept must refuse the request: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"agent_model": "", "agent_reasoning_effort": ""}, s.admin)
	if rec.Code != http.StatusOK || modules.ResolveAgentReasoningEffort(s.db) != "" || modules.ResolveAgentModel(s.db) != "" {
		t.Errorf("clearing the model and the level: %d %s", rec.Code, rec.Body.String())
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
	_, actionable, _ := seededGroupCounts(t, s)
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
		d.Mailboxes[0].Stats.Groups.Open != actionable {
		t.Errorf("mailboxes %+v (actionable %d)", d.Mailboxes, actionable)
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
	if len(d.ActiveJobs) != 0 || len(d.RecentJobs) != 0 || len(d.BusyMailboxIDs) != 0 {
		t.Errorf("jobs should be empty: %+v %+v %v", d.ActiveJobs, d.RecentJobs, d.BusyMailboxIDs)
	}
	// Jobs are shown to administrators only: a queued job appears on the
	// administrator's dashboard, never on a member's.
	if rec := do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "reclassify", "mailbox_id": s.mb.ID}, s.admin); rec.Code != http.StatusCreated {
		t.Fatalf("queue job: %d %s", rec.Code, rec.Body.String())
	}
	var admin DashboardDTO
	decode(t, do(t, s.h, http.MethodGet, "/api/v1/dashboard", nil, s.admin).Body.Bytes(), &admin)
	if len(admin.RecentJobs) == 0 {
		t.Errorf("administrator dashboard lists no job: %+v", admin.RecentJobs)
	}
	var member DashboardDTO
	rec = do(t, s.h, http.MethodGet, "/api/v1/dashboard", nil, s.user)
	decode(t, rec.Body.Bytes(), &member)
	if len(member.ActiveJobs) != 0 || len(member.RecentJobs) != 0 || !strings.Contains(rec.Body.String(), `"active_jobs":[]`) {
		t.Errorf("member dashboard lists jobs: %s", rec.Body.String())
	}
	// Every user still sees which mailbox is busy.
	if len(member.BusyMailboxIDs) != 1 || member.BusyMailboxIDs[0] != s.mb.ID {
		t.Errorf("member busy mailboxes = %v, want [%d]", member.BusyMailboxIDs, s.mb.ID)
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
		if st.Messages != 3 || st.Bounces != 2 || st.Unclassified != 0 || st.Groups.Open != actionable {
			t.Errorf("stats %+v (actionable %d)", st, actionable)
		}
	}
	if !strings.Contains(rec.Body.String(), `"unclassified":0`) || strings.Contains(rec.Body.String(), `"excluded_groups"`) {
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

// TestJobLists: the active list holds every queued and running job; the
// history pages the finished ones, filtered by status, with counts per
// status; members get 403; bad parameters 400.
func TestJobLists(t *testing.T) {
	s := newSeededCore(t)
	insert := func(kind, status string) *models.Job {
		j := &models.Job{Kind: kind, MailboxID: sql.NullInt64{Int64: s.mb.ID, Valid: true}, Status: status, RequestedBy: "test"}
		if err := models.InsertJob(s.db, j); err != nil {
			t.Fatal(err)
		}
		switch status {
		case "done", "error":
			errMsg := ""
			if status == "error" {
				errMsg = "boom"
			}
			if err := models.FinishJob(s.db, j.ID, "r", errMsg); err != nil {
				t.Fatal(err)
			}
		case "canceled":
			if _, err := models.CancelQueuedJob(s.db, j.ID); err != nil {
				t.Fatal(err)
			}
		}
		return j
	}
	insert("fetch", "queued")
	for i := 0; i < 3; i++ {
		insert("group", "done")
	}
	insert("sync", "error")
	insert("reindex", "canceled")

	type page struct {
		Jobs    []JobDTO                 `json:"jobs"`
		Total   int                      `json:"total"`
		Page    int                      `json:"page"`
		PerPage int                      `json:"per_page"`
		Counts  models.FinishedJobCounts `json:"counts"`
	}
	get := func(query string) page {
		t.Helper()
		rec := do(t, s.h, http.MethodGet, "/api/v1/jobs?"+query, nil, s.admin)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body.String())
		}
		var p page
		decode(t, rec.Body.Bytes(), &p)
		return p
	}
	if p := get("state=active"); len(p.Jobs) != 1 || p.Jobs[0].Status != "queued" {
		t.Errorf("active = %+v", p.Jobs)
	}
	all := get("state=finished")
	if all.Total != 5 || len(all.Jobs) != 5 || all.PerPage != 50 || all.Counts != (models.FinishedJobCounts{All: 5, Done: 3, Error: 1, Canceled: 1}) {
		t.Errorf("finished = %+v", all)
	}
	if p := get("state=finished&status=done&per_page=2&page=2"); p.Total != 3 || len(p.Jobs) != 1 || p.Jobs[0].Status != "done" || p.Page != 2 {
		t.Errorf("done page 2 = %+v", p)
	}
	if p := get("state=finished&status=error"); p.Total != 1 || p.Jobs[0].Status != "error" {
		t.Errorf("error = %+v", p)
	}
	if p := get("state=finished&status=canceled"); p.Total != 1 || p.Jobs[0].Status != "canceled" {
		t.Errorf("canceled = %+v", p)
	}
	for _, q := range []string{"state=nope", "state=finished&status=queued", "state=finished&status=running", "state=finished&page=0", "state=finished&per_page=501"} {
		if rec := do(t, s.h, http.MethodGet, "/api/v1/jobs?"+q, nil, s.admin); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", q, rec.Code)
		}
	}
	if rec := do(t, s.h, http.MethodGet, "/api/v1/jobs?state=active", nil, s.user); rec.Code != http.StatusForbidden {
		t.Errorf("active as user: %d, want 403", rec.Code)
	}
}

// TestBusyMailboxIDs: only the jobs that work on the mails of a mailbox make
// it busy; a job for every mailbox makes all of them busy.
func TestBusyMailboxIDs(t *testing.T) {
	mbs := []*models.Mailbox{{ID: 1}, {ID: 2}}
	job := func(kind string, mailbox int64) *models.Job {
		j := &models.Job{Kind: kind}
		if mailbox > 0 {
			j.MailboxID = sql.NullInt64{Int64: mailbox, Valid: true}
		}
		return j
	}
	if got := busyMailboxIDs([]*models.Job{job("fetch", 2), job("analyze", 1), job("notify", 0)}, mbs); len(got) != 1 || got[0] != 2 {
		t.Errorf("busy = %v, want [2]", got)
	}
	if got := busyMailboxIDs([]*models.Job{job("sync", 0)}, mbs); len(got) != 2 {
		t.Errorf("busy for an expansion job = %v, want both", got)
	}
}

// TestGroupListPaging: the group list is paged on the server in a total
// order (pages never overlap), sorted by the requested order, with the
// number of matching groups; bad paging or sort parameters answer 400.
func TestGroupListPaging(t *testing.T) {
	s := newSeededCore(t)
	type page struct {
		Groups  []GroupDTO `json:"groups"`
		Total   int        `json:"total"`
		Page    int        `json:"page"`
		PerPage int        `json:"per_page"`
	}
	get := func(query string) page {
		t.Helper()
		rec := do(t, s.h, http.MethodGet, s.path("/groups?scope=all&"+query), nil, s.user)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body.String())
		}
		var p page
		decode(t, rec.Body.Bytes(), &p)
		return p
	}
	all := get("")
	if all.Total != s.reindex.Groups || len(all.Groups) != all.Total || all.Page != 1 || all.PerPage != 50 {
		t.Fatalf("default page = total %d, %d groups, page %d, per_page %d (want %d groups)",
			all.Total, len(all.Groups), all.Page, all.PerPage, s.reindex.Groups)
	}
	if all.Total < 2 {
		t.Fatalf("the seed needs at least two groups, has %d", all.Total)
	}
	seen := map[string]bool{}
	for n := 1; n <= all.Total; n++ {
		p := get("per_page=1&page=" + itoa(int64(n)))
		if p.Total != all.Total || len(p.Groups) != 1 || p.Groups[0].GroupKey != all.Groups[n-1].GroupKey || seen[p.Groups[0].GroupKey] {
			t.Errorf("page %d = %+v, want group %s", n, p, all.Groups[n-1].GroupKey)
		}
		seen[p.Groups[0].GroupKey] = true
	}
	if p := get("per_page=1&page=" + itoa(int64(all.Total+1))); len(p.Groups) != 0 || p.Total != all.Total {
		t.Errorf("page past the end = %+v", p)
	}
	byCount := get("sort=count_desc")
	for i := 1; i < len(byCount.Groups); i++ {
		a, b := byCount.Groups[i-1], byCount.Groups[i]
		if a.State == b.State && a.MessageCount < b.MessageCount {
			t.Errorf("count_desc not descending: %d before %d", a.MessageCount, b.MessageCount)
		}
	}
	for _, sort := range []string{"last_seen_asc", "severity"} {
		if p := get("sort=" + sort); p.Total != all.Total {
			t.Errorf("sort=%s lists %d groups", sort, p.Total)
		}
	}
	for _, q := range []string{"sort=nope", "page=0", "per_page=0", "per_page=501"} {
		if rec := do(t, s.h, http.MethodGet, s.path("/groups?"+q), nil, s.user); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", q, rec.Code)
		}
	}
}

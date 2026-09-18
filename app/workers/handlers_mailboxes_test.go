package workers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mailcare/app/models"
	"mailcare/app/modules"
)

// TestMailboxBusyAnswersConflict checks that deleting or renaming a mailbox
// while one of its jobs runs answers 409, that other updates still work,
// and that the deletion afterwards cancels the mailbox's queued jobs.
func TestMailboxBusyAnswersConflict(t *testing.T) {
	s := newSeededCore(t)
	running, _, err := modules.EnqueueJob(s.db, modules.JobKindGroup, s.mb.ID, "", "cli", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := models.ClaimJobByID(s.db, running.ID); err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	waiting, _, err := modules.EnqueueJob(s.db, modules.JobKindFetch, s.mb.ID, "", "cli", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, s.h, http.MethodDelete, "/api/v1/mailboxes/"+itoa(s.mb.ID), nil, s.admin)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "a job is running") {
		t.Errorf("delete while running: %d %s", rec.Code, rec.Body.String())
	}
	body := map[string]any{"address": "renamed@example.test", "imap_host": "imap.example.test", "imap_username": "u"}
	rec = do(t, s.h, http.MethodPut, "/api/v1/mailboxes/"+itoa(s.mb.ID), body, s.admin)
	if rec.Code != http.StatusConflict {
		t.Errorf("rename while running: %d %s", rec.Code, rec.Body.String())
	}
	body["address"] = s.mb.Address
	body["display_name"] = "Ops"
	rec = do(t, s.h, http.MethodPut, "/api/v1/mailboxes/"+itoa(s.mb.ID), body, s.admin)
	if rec.Code != http.StatusOK {
		t.Errorf("update without rename while running: %d %s", rec.Code, rec.Body.String())
	}
	if err := models.FinishJob(s.db, running.ID, "done", ""); err != nil {
		t.Fatal(err)
	}
	rec = do(t, s.h, http.MethodDelete, "/api/v1/mailboxes/"+itoa(s.mb.ID), nil, s.admin)
	if rec.Code != http.StatusOK {
		t.Errorf("delete after the job finished: %d %s", rec.Code, rec.Body.String())
	}
	if j, err := models.GetJobByID(s.db, waiting.ID); err != nil || j.Status != modules.JobStatusCanceled {
		t.Errorf("queued job after the delete = %+v (err %v), want canceled", j, err)
	}
}

// TestMessageEndpointsAfterAudit checks the search and HTML endpoint
// details: a search string of any length is accepted, LIKE wildcards in it
// match literally, and a message whose HTML section files are all missing
// answers 404.
func TestMessageEndpointsAfterAudit(t *testing.T) {
	s := newSeededCore(t)
	long := strings.Repeat("x", 500)
	rec := do(t, s.h, http.MethodGet, s.path("/messages?q="+long), nil, s.user)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Errorf("long query: %d %s", rec.Code, rec.Body.String())
	}
	// "%" and "_" are literal characters of the search, not wildcards.
	for _, q := range []string{"%25", "_", "%25%25"} {
		rec = do(t, s.h, http.MethodGet, s.path("/messages?q="+q), nil, s.user)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":0`) {
			t.Errorf("query %s: %d %s", q, rec.Code, rec.Body.String())
		}
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages?q=Undelivered"), nil, s.user)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":1`) {
		t.Errorf("plain query: %d %s", rec.Code, rec.Body.String())
	}
	// The index announces an HTML section whose file does not exist.
	idx, err := s.openIndex(httptest.NewRequest(http.MethodGet, "/", nil), s.mb)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Exec(`UPDATE messages SET html_count = 1 WHERE message_key = ?`, s.keys[0]); err != nil {
		t.Fatal(err)
	}
	idx.Close()
	rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[0]+"/html/1"), nil, s.user)
	if rec.Code != http.StatusNotFound {
		t.Errorf("html with missing section file: %d %s", rec.Code, rec.Body.String())
	}
}

package modules

import (
	"context"
	"errors"
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

// TestGroupsSetStateCommand checks the CLI counterpart of the Web state
// change: "groups set-state ADDRESS KEY STATE" updates the group and
// records the change time; an unknown group or address is an argument
// error.
func TestGroupsSetStateCommand(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	mb, mailsRoot := seedMailbox(t, db, key, "ops@example.test", testSamples)
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	if _, err := jm.RunJob(context.Background(), mailboxJob(JobKindReindex, mb, ""), nil); err != nil {
		t.Fatal(err)
	}
	idx, err := mailengine.OpenIndex(context.Background(), mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := models.ListGroups(idx, models.GroupFilter{})
	idx.Close()
	if err != nil || len(groups) == 0 {
		t.Fatalf("groups: %d, %v", len(groups), err)
	}
	key0 := groups[0].GroupKey
	before := time.Now().Add(-time.Second)

	out := captureStdout(t, func() {
		if err := (&GroupsSetStateCmd{Address: "OPS@example.test", Key: key0, State: GroupStateResolved}).Run(); err != nil {
			t.Errorf("set-state: %v", err)
		}
	})
	if out == "" {
		t.Error("set-state printed nothing")
	}
	idx, err = mailengine.OpenIndex(context.Background(), mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	g, err := models.GetGroup(idx, key0)
	if err != nil || g.State != GroupStateResolved || !g.StateUpdatedAt.Valid || g.StateUpdatedAt.Time.Before(before) {
		t.Errorf("group after set-state = %+v (err %v)", g, err)
	}
	for _, c := range []*GroupsSetStateCmd{
		{Address: mb.Address, Key: "0000000000000000", State: GroupStateOpen},
		{Address: "nobody@example.test", Key: key0, State: GroupStateOpen},
	} {
		var exitErr *ExitError
		if err := c.Run(); !errors.As(err, &exitErr) || exitErr.Code != ExitArgument {
			t.Errorf("set-state %+v: %v, want an argument error", c, err)
		}
	}
	// The list form still works with the address alone (the default
	// subcommand) and with the key.
	out = captureStdout(t, func() {
		if err := (&GroupsListCmd{Address: mb.Address, Scope: GroupScopeAll}).Run(); err != nil {
			t.Errorf("list: %v", err)
		}
		if err := (&GroupsListCmd{Address: mb.Address, Key: key0}).Run(); err != nil {
			t.Errorf("show: %v", err)
		}
	})
	if out == "" {
		t.Error("list printed nothing")
	}
}

// TestJobsCancelCommand checks "jobs cancel ID": a queued job is canceled,
// a job that is not queued is refused with ExitExec and an unknown id with
// ExitArgument.
func TestJobsCancelCommand(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	mb, _ := seedMailbox(t, db, key, "ops@example.test", nil)
	job, _, err := EnqueueJob(db, JobKindFetch, mb.ID, "", "cli", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() {
		if err := (&JobsCancelCmd{ID: job.ID}).Run(); err != nil {
			t.Errorf("cancel: %v", err)
		}
	})
	fresh, err := models.GetJobByID(db, job.ID)
	if err != nil || fresh.Status != JobStatusCanceled || !fresh.FinishedAt.Valid {
		t.Errorf("job after cancel = %+v (err %v)", fresh, err)
	}
	var exitErr *ExitError
	if err := (&JobsCancelCmd{ID: job.ID}).Run(); !errors.As(err, &exitErr) || exitErr.Code != ExitExec {
		t.Errorf("cancel of a canceled job: %v, want ExitExec", err)
	}
	if err := (&JobsCancelCmd{ID: job.ID + 100}).Run(); !errors.As(err, &exitErr) || exitErr.Code != ExitArgument {
		t.Errorf("cancel of an unknown job: %v, want ExitArgument", err)
	}
	// A running job cannot be canceled either.
	running, _, err := EnqueueJob(db, JobKindGroup, mb.ID, "", "cli", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := models.ClaimJobByID(db, running.ID); err != nil || claimed == nil {
		t.Fatal(err)
	}
	if err := (&JobsCancelCmd{ID: running.ID}).Run(); !errors.As(err, &exitErr) || exitErr.Code != ExitExec {
		t.Errorf("cancel of a running job: %v, want ExitExec", err)
	}
	if j, err := models.GetJobByID(db, running.ID); err != nil || j.Status != JobStatusRunning {
		t.Errorf("running job after the refused cancel: %+v (%v)", j, err)
	}
	// The history list (the default subcommand) still runs.
	captureStdout(t, func() {
		if err := (&JobsListCmd{Limit: 5}).Run(); err != nil {
			t.Errorf("jobs list: %v", err)
		}
	})
}

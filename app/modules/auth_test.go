package modules

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"mailcare/app/models"
)

func TestAuthenticateUserByEmail(t *testing.T) {
	db := newTestDB(t)
	alice, err := CreateUserFrom(db, NewUser{Username: "alice", Email: "Alice@Example.com", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := CreateUserFrom(db, NewUser{Username: "bob", Password: "password456"})
	if err != nil {
		t.Fatal(err)
	}

	for _, login := range []string{"alice", "alice@example.com", " ALICE@example.COM "} {
		u, err := AuthenticateUser(db, login, "password123")
		if err != nil || u.ID != alice.ID {
			t.Errorf("login %q: %v", login, err)
		}
	}
	if _, err := AuthenticateUser(db, "alice@example.com", "wrong-password"); err != ErrInvalidCredentials {
		t.Errorf("wrong password by email: %v", err)
	}
	if _, err := AuthenticateUser(db, "nobody@example.com", "password123"); err != ErrInvalidCredentials {
		t.Errorf("unknown email: %v", err)
	}
	if _, err := AuthenticateUser(db, "@", "password123"); err != ErrInvalidCredentials {
		t.Errorf("bare @: %v", err)
	}

	// Addresses are unique among users (case-insensitively).
	if _, err := CreateUserFrom(db, NewUser{Username: "carol", Email: "alice@example.com", Password: "password789"}); !errors.Is(err, models.ErrEmailTaken) {
		t.Errorf("create with a taken address: %v", err)
	}
	taken := "ALICE@example.com"
	if err := UpdateProfile(db, bob, ProfileInput{Email: &taken}); !errors.Is(err, models.ErrEmailTaken) {
		t.Errorf("update to a taken address: %v", err)
	}
	same := "alice@example.com"
	if err := UpdateProfile(db, alice, ProfileInput{Email: &same}); err != nil {
		t.Errorf("keeping one's own address: %v", err)
	}
	empty := ""
	if err := UpdateProfile(db, bob, ProfileInput{Email: &empty}); err != nil {
		t.Errorf("empty address: %v", err)
	}

	// A user who shared the address before the rule existed can still change
	// the other fields while keeping it, but cannot take it anew once cleared.
	if _, err := db.Exec(`UPDATE users SET email = 'alice@example.com' WHERE id = ?`, bob.ID); err != nil {
		t.Fatal(err)
	}
	bob, _ = models.GetUserByID(db, bob.ID)
	lang := "en"
	if err := UpdateProfile(db, bob, ProfileInput{Language: &lang}); err != nil {
		t.Errorf("legacy duplicate keeping its address: %v", err)
	}
	if err := models.UpdateUser(db, bob.ID, bob.DisplayName, bob.Email, "ja", bob.Timezone, bob.Theme, RoleAdmin); err != nil {
		t.Errorf("legacy duplicate, administrator edit: %v", err)
	}
	if err := UpdateProfile(db, bob, ProfileInput{Email: &empty}); err != nil {
		t.Fatal(err)
	}
	bob, _ = models.GetUserByID(db, bob.ID)
	if err := UpdateProfile(db, bob, ProfileInput{Email: &same}); !errors.Is(err, models.ErrEmailTaken) {
		t.Errorf("re-taking a cleared duplicate: %v", err)
	}
	// Updating a user that no longer exists is not an address conflict.
	if err := models.UpdateUserProfile(db, 9999, "x", "alice@example.com", "", "", ""); err != nil {
		t.Errorf("missing user: %v", err)
	}
}

func TestCreateUserEmailRace(t *testing.T) {
	db := newTestDB(t)
	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			_, err := CreateUserFrom(db, NewUser{Username: fmt.Sprintf("u%d", i), Email: "same@example.com", Password: "password123"})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	created := 0
	for err := range errs {
		switch {
		case err == nil:
			created++
		case !errors.Is(err, models.ErrEmailTaken):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if created != 1 {
		t.Errorf("created %d users with the same address, want 1", created)
	}
}

func TestLoginLimiter(t *testing.T) {
	now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	l := NewLoginLimiterWithClock(func() time.Time { return now })
	ip, user := "192.0.2.1", "alice"
	blocked := func(ip, user string) (time.Duration, bool) { return l.Blocked(ip, user) }

	// Below the threshold nothing is held.
	for i := 1; i < LoginFailuresBeforeLock; i++ {
		if wait := l.Fail(ip, user); wait != 0 {
			t.Fatalf("failure %d: wait %s, want none", i, wait)
		}
		if _, b := blocked(ip, user); b {
			t.Fatalf("failure %d: blocked", i)
		}
	}
	// The threshold starts the first wait.
	if wait := l.Fail(ip, user); wait != LoginLockInitial {
		t.Fatalf("failure %d: wait %s, want %s", LoginFailuresBeforeLock, wait, LoginLockInitial)
	}
	if remaining, b := blocked(ip, user); !b || remaining != LoginLockInitial {
		t.Fatalf("blocked = %v %s", b, remaining)
	}
	// The pair is what is held: other users and other clients are free, and
	// the username is matched case-insensitively and trimmed.
	if _, b := blocked(ip, "bob"); b {
		t.Error("another user is held")
	}
	if _, b := blocked("192.0.2.2", user); b {
		t.Error("another client is held")
	}
	if _, b := blocked(ip, " Alice "); !b {
		t.Error("the username must be normalized")
	}
	// The wait runs out with time, then doubles on every further failure up
	// to LoginLockMax.
	now = now.Add(LoginLockInitial - time.Second)
	if remaining, b := blocked(ip, user); !b || remaining != time.Second {
		t.Errorf("one second before the end: %v %s", b, remaining)
	}
	now = now.Add(time.Second)
	if _, b := blocked(ip, user); b {
		t.Error("still blocked after the wait")
	}
	want := LoginLockInitial
	for i := 0; i < 8; i++ {
		want *= 2
		if want > LoginLockMax {
			want = LoginLockMax
		}
		if wait := l.Fail(ip, user); wait != want {
			t.Errorf("failure after the wait: %s, want %s", wait, want)
		}
		now = now.Add(want)
	}
	if wait := l.Fail(ip, user); wait != LoginLockMax {
		t.Errorf("the wait is capped: %s", wait)
	}
	// A success clears everything for the pair.
	l.Reset(ip, user)
	if _, b := blocked(ip, user); b {
		t.Error("blocked after a reset")
	}
	if wait := l.Fail(ip, user); wait != 0 {
		t.Errorf("the count starts over after a reset: %s", wait)
	}
	// Idle records are pruned so the map stays bounded.
	for i := 0; i < loginRecordsPruneEvery*2; i++ {
		l.Fail("198.51.100.1", "user"+string(rune('a'+i%26)))
	}
	now = now.Add(loginRecordTTL + time.Minute)
	l.Fail(ip, "fresh")
	for i := 0; i < loginRecordsPruneEvery; i++ {
		l.Fail(ip, "fresh")
	}
	l.mu.Lock()
	n := len(l.records)
	l.mu.Unlock()
	if n > 2 {
		t.Errorf("%d records kept after the idle period, want the fresh one only", n)
	}
}

package modules

import (
	"testing"
	"time"
)

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

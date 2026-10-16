package modules

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
	"mailcare/app/modules/wording"
)

// notifyKeys returns the occurrences NotifyDue fires at now and records
// them in fired, as the scheduler does.
func notifyKeys(last, now time.Time, s *NotificationSettings, fired map[string]string) []string {
	var out []string
	for _, d := range NotifyDue(last, now, s, fired) {
		fired[d.time] = d.minute
		out = append(out, d.minute)
	}
	return out
}

func TestNotifyDueAndNextNotifyAt(t *testing.T) {
	s := &NotificationSettings{Enabled: true, Times: []string{"09:00"}, IntervalDays: 1}
	tick := func(last, now time.Time, fired map[string]string) (string, bool) {
		keys := notifyKeys(last, now, s, fired)
		if len(keys) == 0 {
			return "", false
		}
		return keys[0], true
	}
	none := func() map[string]string { return map[string]string{} }

	// Disabled: never due.
	s.Enabled = false
	if _, due := tick(localDate(2026, 9, 17, 8, 59, 50), localDate(2026, 9, 17, 9, 0, 10), none()); due {
		t.Errorf("due while disabled")
	}
	s.Enabled = true
	// Before the time: not due. First arrival with no last sent: due.
	if _, due := tick(localDate(2026, 9, 17, 8, 59, 20), localDate(2026, 9, 17, 8, 59, 50), none()); due {
		t.Errorf("due before the notify time")
	}
	fired := none()
	key, due := tick(localDate(2026, 9, 17, 8, 59, 50), localDate(2026, 9, 17, 9, 0, 10), fired)
	if !due || key != "2026-09-17 09:00" {
		t.Errorf("first arrival: %q %v", key, due)
	}
	// The same minute never fires twice, even when the ticks straddle it.
	if _, due := tick(localDate(2026, 9, 17, 8, 59, 50), localDate(2026, 9, 17, 9, 0, 40), fired); due {
		t.Errorf("fired twice within the minute")
	}
	// A restart later that day does not fire retroactively (the occurrence
	// lies before the first tick).
	if _, due := tick(localDate(2026, 9, 17, 12, 0, 0), localDate(2026, 9, 17, 12, 0, 30), none()); due {
		t.Errorf("fired retroactively")
	}
	// Interval 1: sent on the 16th -> the 17th and the 18th are due.
	s.LastSentAt = sql.NullTime{Time: localDate(2026, 9, 16, 9, 0, 5).UTC(), Valid: true}
	if key, due := tick(localDate(2026, 9, 18, 8, 59, 50), localDate(2026, 9, 18, 9, 0, 10), none()); !due || key != "2026-09-18 09:00" {
		t.Errorf("next day with interval 1: %q %v", key, due)
	}
	// Interval 3: days 1 and 2 are skipped, day 3 fires. A mail sent late
	// in the evening still counts by its calendar day.
	s.IntervalDays = 3
	s.LastSentAt = sql.NullTime{Time: localDate(2026, 9, 17, 23, 30, 0).UTC(), Valid: true}
	for _, day := range []int{18, 19} {
		if _, due := tick(localDate(2026, 9, day, 8, 59, 50), localDate(2026, 9, day, 9, 0, 10), none()); due {
			t.Errorf("due on day %d with interval 3", day)
		}
	}
	if _, due := tick(localDate(2026, 9, 20, 8, 59, 50), localDate(2026, 9, 20, 9, 0, 10), none()); !due {
		t.Errorf("not due on day 3 with interval 3")
	}
	// A tick spanning midnight still catches yesterday's occurrence.
	s.Times = []string{"23:59"}
	s.LastSentAt = sql.NullTime{}
	if key, due := tick(localDate(2026, 9, 20, 23, 58, 50), localDate(2026, 9, 21, 0, 0, 10), none()); !due || key != "2026-09-20 23:59" {
		t.Errorf("midnight span: %q %v", key, due)
	}

	// NextNotifyAt.
	s = &NotificationSettings{Enabled: false, Times: []string{"09:00"}, IntervalDays: 1}
	if _, ok := NextNotifyAt(localDate(2026, 9, 17, 8, 0, 0), s); ok {
		t.Errorf("next while disabled")
	}
	s.Enabled = true
	if next, ok := NextNotifyAt(localDate(2026, 9, 17, 8, 0, 0), s); !ok || !next.Equal(localDate(2026, 9, 17, 9, 0, 0)) {
		t.Errorf("next before the time = %v %v", next, ok)
	}
	if next, _ := NextNotifyAt(localDate(2026, 9, 17, 9, 0, 0), s); !next.Equal(localDate(2026, 9, 18, 9, 0, 0)) {
		t.Errorf("next at the time = %v", next)
	}
	s.IntervalDays = 3
	s.LastSentAt = sql.NullTime{Time: localDate(2026, 9, 17, 9, 0, 5).UTC(), Valid: true}
	if next, _ := NextNotifyAt(localDate(2026, 9, 17, 10, 0, 0), s); !next.Equal(localDate(2026, 9, 20, 9, 0, 0)) {
		t.Errorf("next with interval 3 = %v", next)
	}
	if next, _ := NextNotifyAt(localDate(2026, 9, 25, 10, 0, 0), s); !next.Equal(localDate(2026, 9, 26, 9, 0, 0)) {
		t.Errorf("next long after the last mail = %v", next)
	}
}

// TestNotifyTwoTimesADay: with two times every listed time of a
// notification day fires once, also the second one on the day the first one
// sent the mail; every time has a separate guard; with an interval of 2 days
// both times fire on every other day.
func TestNotifyTwoTimesADay(t *testing.T) {
	s := &NotificationSettings{Enabled: true, Times: []string{"09:00", "18:00"}, IntervalDays: 1}
	fired := map[string]string{}
	at := func(day, h, m int) time.Time { return localDate(2026, 9, day, h, m, 0) }
	// Ticks every 30 seconds over two days, recording a mail whenever a
	// notification fires (as SendNotification does).
	run := func(from, to time.Time) []string {
		var sent []string
		for now := from.Add(30 * time.Second); !now.After(to); now = now.Add(30 * time.Second) {
			if keys := notifyKeys(now.Add(-30*time.Second), now, s, fired); len(keys) > 0 {
				sent = append(sent, keys...)
				s.LastSentAt = sql.NullTime{Time: now.UTC(), Valid: true}
			}
		}
		return sent
	}
	got := run(at(17, 0, 0), at(19, 0, 0))
	want := []string{"2026-09-17 09:00", "2026-09-17 18:00", "2026-09-18 09:00", "2026-09-18 18:00"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("interval 1: sent at %v, want %v", got, want)
	}
	// The guard is per time: the 18:00 of a day does not hide its 09:00.
	if fired["09:00"] != "2026-09-18 09:00" || fired["18:00"] != "2026-09-18 18:00" {
		t.Errorf("guards %v", fired)
	}
	if keys := notifyKeys(at(18, 17, 59), at(18, 18, 1), s, fired); len(keys) != 0 {
		t.Errorf("18:00 fired twice: %v", keys)
	}
	// Interval 2: the 19th is skipped, the 20th sends at both times.
	s.IntervalDays = 2
	got = run(at(19, 0, 0), at(21, 0, 0))
	want = []string{"2026-09-20 09:00", "2026-09-20 18:00"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("interval 2: sent at %v, want %v", got, want)
	}
	// One tick covering both times (the server was asleep in between)
	// fires both at once.
	s.LastSentAt = sql.NullTime{}
	fired = map[string]string{}
	if keys := notifyKeys(at(22, 8, 0), at(22, 19, 0), s, fired); strings.Join(keys, ",") != "2026-09-22 09:00,2026-09-22 18:00" {
		t.Errorf("one long tick: %v", keys)
	}

	// next_send_at: the next listed time of a notification day.
	s = &NotificationSettings{Enabled: true, Times: []string{"09:00", "18:00"}, IntervalDays: 2,
		LastSentAt: sql.NullTime{Time: at(17, 9, 0).UTC(), Valid: true}}
	for _, c := range []struct{ now, want time.Time }{
		{at(17, 10, 0), at(17, 18, 0)}, // the second time of the day the mail went out
		{at(17, 18, 0), at(19, 9, 0)},  // the 18th is no notification day
		{at(19, 9, 30), at(19, 18, 0)},
		{at(25, 20, 0), at(26, 9, 0)},
	} {
		if next, ok := NextNotifyAt(c.now, s); !ok || !next.Equal(c.want) {
			t.Errorf("next after %v = %v %v, want %v", c.now, next, ok, c.want)
		}
	}
}

func TestNotificationSettingsSaveAndResolve(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := CreateUserFrom(db, NewUser{Username: "admin", Email: "Admin@Example.test", Password: "password123", Role: RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if admin.Email != "admin@example.test" {
		t.Errorf("email not normalized: %q", admin.Email)
	}
	// Defaults.
	s, err := ResolveNotificationSettings(db, key)
	if err != nil {
		t.Fatal(err)
	}
	if s.SMTP.Port != DefaultSMTPPort || s.SMTP.Security != DefaultSMTPSecurity || s.Enabled || FormatCheckTimes(s.Times) != DefaultNotifyTimes ||
		s.IntervalDays != DefaultNotifyIntervalDays || len(s.UserIDs) != 0 || s.SMTPPasswordSet || s.LastSentAt.Valid {
		t.Errorf("defaults: %+v", s)
	}
	if got := EffectiveBaseURL(db, s); got != "http://localhost:9790" {
		t.Errorf("effective base URL = %q", got)
	}
	// Invalid inputs.
	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }
	ids := func(v ...int64) *[]int64 { return &v }
	for name, in := range map[string]*NotificationInput{
		"port":     {SMTPPort: num(70000)},
		"security": {SMTPSecurity: str("tls")},
		"from":     {SMTPFrom: str("not an address")},
		"url":      {PublicBaseURL: str("mailcare.example.com")},
		"urlq":     {PublicBaseURL: str("https://x.test/?a=1")},
		"time":     {Times: &[]string{"25:00"}},
		"times":    {Times: &[]string{"09:00,9"}},
		"interval": {IntervalDays: num(8)},
		"user":     {UserIDs: ids(admin.ID, 999)},
		"host":     {SMTPHost: str("smtp host")},
	} {
		if err := SaveNotificationSettings(db, key, in); err == nil {
			t.Errorf("%s: invalid input accepted", name)
		}
	}
	// Valid update; the password is stored encrypted and never in clear.
	on := true
	in := &NotificationInput{SMTPHost: str(" smtp.example.test "), SMTPPort: num(2525), SMTPSecurity: str("NONE"),
		SMTPUsername: str("bounce"), SMTPPassword: str("s3cret"), SMTPFrom: str("MailCare@Example.test"),
		PublicBaseURL: str("https://mailcare.example.test/"), Enabled: &on, Times: &[]string{"18:00", "7:30, 07:30"}, IntervalDays: num(3),
		UserIDs: ids(admin.ID, admin.ID)}
	if err := SaveNotificationSettings(db, key, in); err != nil {
		t.Fatal(err)
	}
	if models.GetSetting(db, SettingSMTPPasswordEnc) == "s3cret" || models.GetSetting(db, SettingSMTPPasswordEnc) == "" {
		t.Errorf("password stored in clear or not at all")
	}
	s, err = ResolveNotificationSettings(db, key)
	if err != nil {
		t.Fatal(err)
	}
	if s.SMTP.Host != "smtp.example.test" || s.SMTP.Port != 2525 || s.SMTP.Security != IMAPSecurityNone || s.SMTP.Username != "bounce" ||
		s.SMTP.Password != "s3cret" || !s.SMTPPasswordSet || s.SMTP.From != "mailcare@example.test" ||
		s.PublicBaseURL != "https://mailcare.example.test" || !s.Enabled || FormatCheckTimes(s.Times) != "07:30,18:00" || s.IntervalDays != 3 ||
		len(s.UserIDs) != 1 || s.UserIDs[0] != admin.ID {
		t.Errorf("resolved: %+v", s)
	}
	if got := EffectiveBaseURL(db, s); got != "https://mailcare.example.test" {
		t.Errorf("effective base URL = %q", got)
	}
	// Without the key the password stays hidden but is reported as set.
	if s, _ = ResolveNotificationSettings(db, nil); s.SMTP.Password != "" || !s.SMTPPasswordSet {
		t.Errorf("without key: %+v", s.SMTP)
	}
	// An empty password keeps the stored one; default values delete rows.
	if err := SaveNotificationSettings(db, key, &NotificationInput{SMTPPassword: str(""), IntervalDays: num(DefaultNotifyIntervalDays)}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := models.GetSettingStrict(db, SettingNotifyInterval); found {
		t.Errorf("default interval stored")
	}
	if s, _ = ResolveNotificationSettings(db, key); s.SMTP.Password != "s3cret" {
		t.Errorf("password lost on empty update")
	}
	// While a password is stored, a change of the connection settings must
	// carry the password (nothing is saved otherwise); with it the change
	// goes through and a default value still deletes its row.
	if err := SaveNotificationSettings(db, key, &NotificationInput{SMTPPort: num(DefaultSMTPPort), SMTPFrom: str("other@example.test")}); !errors.Is(err, ErrSMTPPasswordRequired) {
		t.Errorf("port change without the password: %v", err)
	}
	if s, _ = ResolveNotificationSettings(db, key); s.SMTP.Port != 2525 || s.SMTP.From != "mailcare@example.test" {
		t.Errorf("refused update changed something: %+v", s.SMTP)
	}
	if err := SaveNotificationSettings(db, key, &NotificationInput{SMTPPort: num(DefaultSMTPPort), SMTPPassword: str("s3cret")}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := models.GetSettingStrict(db, SettingSMTPPort); found {
		t.Errorf("default port stored")
	}
	// The password belongs to the username: "settings set smtp_password \"\""
	// is refused while the username stays, and clearing the username
	// removes the stored password without asking for it.
	if _, err := ApplySetting(db, settingSMTPPassword, "", nil); !errors.Is(err, errSMTPPasswordMissing) &&
		(err == nil || !strings.Contains(err.Error(), errSMTPPasswordMissing.Error())) {
		t.Errorf("password removed while the username stays: %v", err)
	}
	if _, err := ApplySetting(db, SettingSMTPUsername, "", nil); err != nil {
		t.Fatal(err)
	}
	if s, _ = ResolveNotificationSettings(db, key); s.SMTPPasswordSet || s.SMTP.Username != "" {
		t.Errorf("password not cleared with the username: %+v", s.SMTP)
	}
	// Recipients: only users with an address are addressed.
	bob, err := CreateUserFrom(db, NewUser{Username: "bob", Password: "password123", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveNotificationSettings(db, key, &NotificationInput{UserIDs: ids(bob.ID, admin.ID)}); err != nil {
		t.Fatal(err)
	}
	s, _ = ResolveNotificationSettings(db, nil)
	selected, addresses, err := NotificationRecipients(db, s)
	if err != nil || len(selected) != 2 || len(addresses) != 1 || addresses[0] != "admin@example.test" {
		t.Errorf("recipients: %d selected, %v, %v", len(selected), addresses, err)
	}
	// Email validation.
	for _, bad := range []string{"no-at", "a@b", "a b@example.test", "<a@example.test>", strings.Repeat("a", 250) + "@x.test"} {
		if _, err := NormalizeEmail(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if got, err := NormalizeEmail("  Ops@Example.TEST "); err != nil || got != "ops@example.test" {
		t.Errorf("NormalizeEmail = %q, %v", got, err)
	}
	empty := ""
	if err := UpdateProfile(db, bob, ProfileInput{Email: &empty}); err != nil {
		t.Errorf("clear email: %v", err)
	}
	if fresh, _ := models.GetUserByID(db, bob.ID); fresh.Email != "" {
		t.Errorf("email not cleared: %q", fresh.Email)
	}
}

func TestApplySettingValidatesKeys(t *testing.T) {
	db := newTestDB(t)
	admin, err := CreateUserFrom(db, NewUser{Username: "admin", Password: "password123", Role: RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	loadKey := func() ([]byte, error) { return LoadSecretKey(db) }
	isArg := func(err error) bool {
		var exitErr *ExitError
		return errors.As(err, &exitErr) && exitErr.Code == ExitArgument
	}
	// Unknown keys and invalid values are argument errors.
	for _, bad := range [][2]string{
		{"bogus", "1"}, {SettingSMTPPort, "abc"}, {SettingSMTPPort, "0"}, {SettingSMTPSecurity, "tls"},
		{SettingSMTPFrom, "nope"}, {SettingPublicBaseURL, "example.com"}, {SettingNotifyEnabled, "maybe"},
		{SettingNotifyTime, "9"}, {SettingNotifyInterval, "0"}, {SettingNotifyInterval, "x"},
		{SettingNotifyUserIDs, "1,a"}, {SettingNotifyUserIDs, "999"}, {SettingWorkers, "0"}, {SettingCheckTimes, "25:00"},
	} {
		if _, err := ApplySetting(db, bad[0], bad[1], loadKey); !isArg(err) {
			t.Errorf("%s=%s: %v, want an argument error", bad[0], bad[1], err)
		}
	}
	// Valid values are normalized and stored.
	for _, ok := range [][3]string{
		{SettingSMTPHost, "smtp.example.test", "smtp.example.test"},
		{SettingSMTPPort, "465", "465"},
		{SettingSMTPSecurity, "SSL", "ssl"},
		{SettingSMTPFrom, "MailCare@Example.test", "mailcare@example.test"},
		{SettingPublicBaseURL, "https://mailcare.example.test/", "https://mailcare.example.test"},
		{SettingNotifyEnabled, "yes", "true"},
		{SettingNotifyTime, "", DefaultNotifyTimes},
		{SettingNotifyTime, "18:00, 7:05,07:05", "07:05,18:00"},
		{SettingNotifyTime, "7:05", "07:05"},
		{SettingNotifyInterval, "2", "2"},
		{SettingNotifyUserIDs, "1, 1", "1"},
	} {
		got, err := ApplySetting(db, ok[0], ok[1], loadKey)
		if err != nil || got != ok[2] {
			t.Errorf("%s=%s: %q, %v (want %q)", ok[0], ok[1], got, err, ok[2])
		}
	}
	// A username needs a password: without one to ask for it is refused, and
	// the password alone has no username to belong to.
	if _, err := ApplySetting(db, SettingSMTPUsername, "bounce", loadKey); !isArg(err) || !strings.Contains(err.Error(), errSMTPPasswordMissing.Error()) {
		t.Errorf("username without a password: %v", err)
	}
	if _, err := ApplySetting(db, settingSMTPPassword, "s3cret", loadKey); !isArg(err) || !strings.Contains(err.Error(), SettingSMTPUsername) {
		t.Errorf("password without a username: %v", err)
	}
	asked := 0
	ask := func() (string, error) { asked++; return "s3cret", nil }
	if got, err := applySetting(db, SettingSMTPUsername, "bounce", loadKey, ask); err != nil || got != "bounce" || asked != 1 {
		t.Errorf("username with the password asked for: %q %v (asked %d)", got, err, asked)
	}
	if got, err := ApplySetting(db, settingSMTPPassword, "s3cret", loadKey); err != nil || got != "(encrypted)" {
		t.Errorf("password change: %q %v", got, err)
	}
	key, _ := LoadSecretKey(db)
	s, err := ResolveNotificationSettings(db, key)
	if err != nil {
		t.Fatal(err)
	}
	if s.SMTP.Host != "smtp.example.test" || s.SMTP.Port != 465 || s.SMTP.Security != IMAPSecuritySSL || s.SMTP.Username != "bounce" ||
		s.SMTP.Password != "s3cret" || s.SMTP.From != "mailcare@example.test" || s.PublicBaseURL != "https://mailcare.example.test" ||
		!s.Enabled || FormatCheckTimes(s.Times) != "07:05" || s.IntervalDays != 2 || len(s.UserIDs) != 1 || s.UserIDs[0] != admin.ID {
		t.Errorf("resolved after CLI: %+v", s)
	}
	// With a password stored, a connection key cannot change without the
	// password (one key per command: the command asks for it); other keys
	// and the unchanged value still work.
	for _, change := range [][2]string{{SettingSMTPHost, "other.example.test"}, {SettingSMTPPort, "25"}, {SettingSMTPSecurity, "none"}, {SettingSMTPUsername, "other"}} {
		_, err := ApplySetting(db, change[0], change[1], loadKey)
		if !isArg(err) || !strings.Contains(err.Error(), ErrSMTPPasswordRequired.Error()) || !strings.Contains(err.Error(), "asks for the password") {
			t.Errorf("%s=%s with a stored password: %v", change[0], change[1], err)
		}
	}
	if got, err := ApplySetting(db, SettingSMTPHost, "smtp.example.test", loadKey); err != nil || got != "smtp.example.test" {
		t.Errorf("unchanged host: %q %v", got, err)
	}
	if got, err := ApplySetting(db, SettingSMTPFrom, "other@example.test", loadKey); err != nil || got != "other@example.test" {
		t.Errorf("sender change: %q %v", got, err)
	}
	if s, _ := ResolveNotificationSettings(db, key); s.SMTP.Host != "smtp.example.test" || s.SMTP.Port != 465 || s.SMTP.Username != "bounce" {
		t.Errorf("refused CLI changes were applied: %+v", s.SMTP)
	}
	// Asked for, the password comes with the change.
	if got, err := applySetting(db, SettingSMTPPort, "587", loadKey, ask); err != nil || got != "587" || asked != 2 {
		t.Errorf("port change with the password asked for: %q %v (asked %d)", got, err, asked)
	}
	// An empty value restores the default through the same validation: the
	// password stays while the username does, the sender and host stay while
	// notifications are on, and clearing the username removes the password.
	for _, refused := range []string{settingSMTPPassword, SettingSMTPFrom, SettingSMTPHost} {
		if _, err := ApplySetting(db, refused, "", nil); !isArg(err) {
			t.Errorf("%s=\"\": %v, want an argument error", refused, err)
		}
	}
	if _, err := ApplySetting(db, SettingSMTPUsername, "", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := ApplySetting(db, SettingSMTPHost, "other.example.test", loadKey); err != nil || got != "other.example.test" {
		t.Errorf("host change once the username and the password are removed: %q %v", got, err)
	}
	if _, err := ApplySetting(db, SettingNotifyInterval, "", nil); err != nil {
		t.Fatal(err)
	}
	if s, _ = ResolveNotificationSettings(db, key); s.SMTPPasswordSet || s.SMTP.Username != "" || s.IntervalDays != DefaultNotifyIntervalDays {
		t.Errorf("defaults not restored: %+v", s)
	}
	if _, err := ApplySetting(db, SettingNotifyEnabled, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplySetting(db, SettingSMTPHost, "", nil); err != nil {
		t.Errorf("host removed once notifications are off: %v", err)
	}
	// The password needs the master key.
	if _, err := applySetting(db, SettingSMTPUsername, "bounce", nil, ask); err == nil {
		t.Errorf("password stored without a key")
	}
}

// seedNotifiableIndex rebuilds the index of a mailbox from the samples and
// gives every actionable group a completed report (the first one with an
// empty summary, so the fallback to the report body is exercised). It
// returns the actionable groups.
func seedNotifiableIndex(t *testing.T, jm *JobManager, mb *models.Mailbox, mailsRoot string) []*models.BounceGroup {
	t.Helper()
	if _, err := jm.RunJob(context.Background(), mailboxJob(JobKindReindex, mb, ""), nil); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	idx, err := mailengine.OpenIndex(context.Background(), mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	actionable := true
	groups, err := models.ListGroups(idx, models.GroupFilter{Actionable: &actionable})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) == 0 {
		// The samples are grouped by the category rules; make sure at least
		// one group is actionable so the mail has a block to render.
		all, err := models.ListGroups(idx, models.GroupFilter{})
		if err != nil || len(all) == 0 {
			t.Fatalf("no groups after reindex: %v", err)
		}
		if _, err := idx.Exec(`UPDATE groups SET actionable = 1 WHERE group_key = ?`, all[0].GroupKey); err != nil {
			t.Fatal(err)
		}
		groups = all[:1]
	}
	for i, g := range groups {
		r := &models.AgentReport{GroupKey: g.GroupKey, Provider: "test", MessageCount: g.MessageCount}
		if err := models.InsertAgentReport(idx, r); err != nil {
			t.Fatal(err)
		}
		summary := "summary of " + g.GroupKey
		// The first report could not establish the cause (confidence low).
		confidence := ""
		if i == 0 {
			summary = ""
			confidence = "low"
		}
		markdown := "# 原因の分析\n\nfirst paragraph of " + g.GroupKey + "\ncontinued\n\nsecond paragraph\n"
		if err := models.CompleteAgentReport(idx, r.ID, summary, ResponsibleSender, "high", confidence, markdown); err != nil {
			t.Fatal(err)
		}
	}
	return groups
}

func TestSendNotificationEndToEnd(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	mb, mailsRoot := seedMailbox(t, db, key, "ops@example.test", testSamples)
	// A second mailbox without an index is skipped silently.
	if _, err := CreateMailbox(db, key, &MailboxInput{Address: "empty@example.test", ImapHost: "h", ImapUsername: "u", ImapPassword: "p"}); err != nil {
		t.Fatal(err)
	}
	admin, _ := CreateUserFrom(db, NewUser{Username: "admin", Email: "admin@example.test", Password: "password123", Role: RoleAdmin})
	// carol reads English in New York; admin keeps the defaults (Japanese, Tokyo).
	carol, _ := CreateUserFrom(db, NewUser{Username: "carol", Email: "carol@example.test", Password: "password123", Role: RoleUser,
		Language: "en", Timezone: "America/New_York"})
	bob, _ := CreateUserFrom(db, NewUser{Username: "bob", Password: "password123", Role: RoleUser})
	srv := startFakeSMTP(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 9, 0, 0, 0, time.Local)

	// Every send condition, in the order they are checked.
	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }
	expectSkip := func(want string) {
		t.Helper()
		res, err := SendNotification(ctx, db, key, mailsRoot, now)
		if err != nil {
			t.Fatalf("SendNotification: %v", err)
		}
		if res.Sent || res.Skipped != want {
			t.Errorf("skipped = %q, want %q", res.Skipped, want)
		}
	}
	// notify_enabled only gates the schedule: sending now works while off.
	expectSkip(SkipSMTPNotConfigured)
	off := false
	if err := SaveNotificationSettings(db, key, &NotificationInput{SMTPHost: str(srv.Host()), SMTPPort: num(srv.Port()),
		SMTPSecurity: str(IMAPSecurityNone), SMTPFrom: str("mailcare@example.test"), Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	expectSkip(SkipNoRecipient)
	ids := []int64{bob.ID}
	if err := SaveNotificationSettings(db, key, &NotificationInput{UserIDs: &ids}); err != nil {
		t.Fatal(err)
	}
	expectSkip(SkipRecipientsNoEmail)
	ids = []int64{bob.ID, carol.ID, admin.ID}
	if err := SaveNotificationSettings(db, key, &NotificationInput{UserIDs: &ids}); err != nil {
		t.Fatal(err)
	}
	expectSkip(SkipNoActionableGroups)
	if len(srv.Messages()) != 0 {
		t.Fatalf("a mail was sent while a condition failed")
	}

	groups := seedNotifiableIndex(t, jm, mb, mailsRoot)
	res, err := SendNotification(ctx, db, key, mailsRoot, now)
	if err != nil {
		t.Fatalf("SendNotification: %v", err)
	}
	if !res.Sent || res.Groups != len(groups) || strings.Join(res.Recipients, ",") != "admin@example.test,carol@example.test" || len(res.Failures) != 0 {
		t.Errorf("result %+v", res)
	}
	// One mail per recipient, each addressed to that recipient only.
	msgs := srv.Messages()
	if len(msgs) != 2 {
		t.Fatalf("mails sent: %d, want one per recipient", len(msgs))
	}
	byTo := map[string]string{}
	for _, m := range msgs {
		if len(m.To) != 1 || m.From != "mailcare@example.test" {
			t.Errorf("envelope %+v", m)
		}
		byTo[m.To[0]] = m.Data
	}
	for _, c := range []struct {
		to, language, subjectPart, reviewMark, periodPart, dateOpen, dateClose string
	}{
		// admin: Japanese, dates in Asia/Tokyo; the date in full-width parentheses.
		{"admin@example.test", "ja", " " + strconv.Itoa(len(groups)) + " ", "\u3010\u8981\u78ba\u8a8d\u3011", "\u671f\u9593: ", "\uff08", "\uff09"},
		// carol: English, dates in America/New_York.
		{"carol@example.test", "en", " " + strconv.Itoa(len(groups)) + " alert(s) need attention ", "[Needs review] ", "Period: ", "(", ")"},
	} {
		data, ok := byTo[c.to]
		if !ok {
			t.Fatalf("no mail to %s", c.to)
		}
		loc := MailLocaleFor(&models.User{Language: c.language, Timezone: map[string]string{"ja": "Asia/Tokyo", "en": "America/New_York"}[c.language]})
		// The date is the recipient's: 9:00 in the server zone may still be
		// the day before in New York.
		subject := decodeSubject(t, data)
		if date := c.dateOpen + now.In(loc.Location).Format("2006-01-02") + c.dateClose; !strings.HasPrefix(subject, NotifyMailSubjectPrefix) ||
			!strings.Contains(subject, c.subjectPart) || !strings.Contains(subject, date) {
			t.Errorf("%s: subject = %q, want the date %s", c.to, subject, date)
		}
		body := data[strings.Index(data, "\r\n\r\n")+4:]
		if !strings.Contains(body, mb.Address) || strings.Contains(body, "empty@example.test") {
			t.Errorf("%s: mailbox headings wrong:\n%s", c.to, body)
		}
		labels, err := loadCategoryLabels(c.language)
		if err != nil {
			t.Fatal(err)
		}
		for i, g := range groups {
			url := GroupURL("http://localhost:9790", mb.ID, g.GroupKey)
			if !strings.Contains(body, url) {
				t.Errorf("%s: body lacks the URL %s:\n%s", c.to, url, body)
			}
			if label := labels[g.Category]; label == "" || !strings.Contains(body, label+": "+g.UnitValue) {
				t.Errorf("%s: body lacks the category block %q for %s:\n%s", c.to, label, g.Category, body)
			}
			// The summary stays as the agent wrote it, whatever the language.
			want := "summary of " + g.GroupKey
			if i == 0 {
				want = c.reviewMark + "first paragraph of " + g.GroupKey + " continued"
			}
			if !strings.Contains(body, want) {
				t.Errorf("%s: body lacks the summary %q:\n%s", c.to, want, body)
			}
			if period := c.periodPart + localStamp(g.FirstSeen, loc.Location); !strings.Contains(body, period) {
				t.Errorf("%s: body lacks the period in the recipient's time zone %q:\n%s", c.to, period, body)
			}
		}
		if n := strings.Count(body, c.reviewMark); n != 1 {
			t.Errorf("%s: the needs-review mark appears %d times, want once (the low-confidence report)", c.to, n)
		}
		if strings.Count(body, "URL: ") != len(groups) {
			t.Errorf("%s: %d URL lines, want %d:\n%s", c.to, strings.Count(body, "URL: "), len(groups), body)
		}
		if strings.Contains(data, "<no value>") || strings.Contains(body, "{{") {
			t.Errorf("%s: template left placeholders:\n%s", c.to, body)
		}
	}
	// One recipient refused: the other still gets the mail, the failure is
	// reported and the send counts; every recipient refused is an error.
	srv.RejectRecipients = []string{"carol@example.test"}
	res, err = SendNotification(ctx, db, key, mailsRoot, now)
	if err != nil || !res.Sent || strings.Join(res.Recipients, ",") != "admin@example.test" ||
		len(res.Failures) != 1 || !strings.HasPrefix(res.Failures[0], "carol@example.test: ") {
		t.Errorf("partial failure: %+v (err %v)", res, err)
	}
	srv.RejectRecipients = []string{"carol@example.test", "admin@example.test"}
	if res, err = SendNotification(ctx, db, key, mailsRoot, now); err == nil || res.Sent || len(res.Failures) != 2 {
		t.Errorf("total failure: %+v (err %v)", res, err)
	}
	srv.RejectRecipients = nil
	// The send time is recorded and the settings expose it.
	s, _ := ResolveNotificationSettings(db, nil)
	if !s.LastSentAt.Valid || !s.LastSentAt.Time.Equal(now) {
		t.Errorf("last sent = %+v", s.LastSentAt)
	}
	// A resolved group drops out of the mail; ignored ones too.
	idx, err := mailengine.OpenIndex(ctx, mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		change := GroupStateChange{State: GroupStateResolved, Reason: sql.NullString{String: models.ResolveActionDNSFixed, Valid: true}}
		if err := ChangeGroupState(idx, g.GroupKey, change, ""); err != nil {
			t.Fatal(err)
		}
	}
	idx.Close()
	expectSkip(SkipNoActionableGroups)
	// A resolved group sent back for a re-check comes back in a separate section
	// with what was done, whatever its report.
	idx, err = mailengine.OpenIndex(ctx, mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := models.SetGroupNeedsRecheck(idx, groups[0].GroupKey); err != nil {
		t.Fatal(err)
	}
	idx.Close()
	for _, c := range []struct {
		language, heading, reason string
	}{
		{"ja", "[対応済（再確認）]", "対応内容: 送信ドメインの DNS 設定（SPF・DKIM・DMARC）を修正した"},
		{"en", "[Resolved (re-check)]", "Action taken: Corrected the DNS records of the sending domain (SPF, DKIM, DMARC)"},
	} {
		n, err := BuildNotification(ctx, db, mailsRoot, "http://localhost:9790", now, MailLocale{Language: c.language, Location: time.UTC})
		if err != nil {
			t.Fatal(err)
		}
		if n.Total != 1 || !strings.Contains(n.Body, c.heading) || !strings.Contains(n.Body, c.reason) ||
			strings.Contains(n.Body, "[未対応]") || strings.Contains(n.Body, "[Open]") ||
			!strings.Contains(n.Body, GroupURL("http://localhost:9790", mb.ID, groups[0].GroupKey)) {
			t.Errorf("%s: re-check mail (total %d):\n%s", c.language, n.Total, n.Body)
		}
	}
	idx, err = mailengine.OpenIndex(ctx, mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	change := GroupStateChange{State: GroupStateResolved, Reason: sql.NullString{String: models.ResolveActionDNSFixed, Valid: true}}
	if err := ChangeGroupState(idx, groups[0].GroupKey, change, ""); err != nil {
		t.Fatal(err)
	}
	idx.Close()
	expectSkip(SkipNoActionableGroups)
	// The public base URL is used for links when set.
	if err := SaveNotificationSettings(db, key, &NotificationInput{PublicBaseURL: str("https://mailcare.example.test")}); err != nil {
		t.Fatal(err)
	}
	s, _ = ResolveNotificationSettings(db, nil)
	n, err := BuildNotification(ctx, db, mailsRoot, EffectiveBaseURL(db, s), now, DefaultMailLocale())
	if err != nil || n.Total != 0 || !strings.Contains(n.Body, "https://mailcare.example.test/settings/notifications") {
		t.Errorf("build with base URL: total %d, err %v\n%s", n.Total, err, n.Body)
	}
}

func TestNotifyJobRunsThroughTheManager(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	srv := startFakeSMTP(t)
	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }
	if err := SaveNotificationSettings(db, key, &NotificationInput{SMTPHost: str(srv.Host()), SMTPPort: num(srv.Port()),
		SMTPSecurity: str(IMAPSecurityNone), SMTPFrom: str("mailcare@example.test")}); err != nil {
		t.Fatal(err)
	}
	// Enqueue validation: no mailbox, a plain or test target only.
	mb, mailsRoot := seedMailbox(t, db, key, "ops@example.test", testSamples)
	if _, _, err := jm.Enqueue(JobKindNotify, mb.ID, "", "t"); err == nil || !strings.Contains(err.Error(), "no mailbox") {
		t.Errorf("notify with a mailbox: %v", err)
	}
	if _, _, err := jm.Enqueue(JobKindNotify, 0, "bogus", "t"); err == nil {
		t.Errorf("bogus target accepted")
	}
	if _, _, err := jm.Enqueue(JobKindNotify, 0, "test:nope", "t"); err == nil {
		t.Errorf("invalid test address accepted")
	}
	// A test mail through the manager.
	job, created, err := jm.Enqueue(JobKindNotify, 0, "test: Admin@Example.test", "web:admin")
	if err != nil || !created || job.Target != "test:admin@example.test" {
		t.Fatalf("enqueue test: %+v %v %v", job, created, err)
	}
	if keys := jm.resourceKeys(job); len(keys) != 1 || keys[0] != lockNotify {
		t.Errorf("resource keys = %v", keys)
	}
	claimed, err := jm.claimByID(job.ID)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v", err)
	}
	jm.execute(context.Background(), claimed, nil)
	jm.releaseLocks(claimed.ID)
	if claimed.Status != JobStatusDone || claimed.Result != "sent a test mail to admin@example.test" {
		t.Errorf("test job: %s %q %q", claimed.Status, claimed.Result, claimed.ErrorMessage)
	}
	if msgs := srv.Messages(); len(msgs) != 1 || msgs[0].To[0] != "admin@example.test" {
		t.Errorf("test mail: %+v", msgs)
	}
	// The notification job records a skipped reason without failing.
	result, err := jm.RunJob(context.Background(), &models.Job{Kind: JobKindNotify}, nil)
	if err != nil || result != "skipped: "+SkipNoRecipient {
		t.Errorf("skipped notification: %q, %v", result, err)
	}
	// And sends when every condition holds, even with the schedule disabled.
	admin, _ := CreateUserFrom(db, NewUser{Username: "admin", Email: "admin@example.test", Password: "password123", Role: RoleAdmin})
	ids := []int64{admin.ID}
	if err := SaveNotificationSettings(db, key, &NotificationInput{UserIDs: &ids}); err != nil {
		t.Fatal(err)
	}
	groups := seedNotifiableIndex(t, jm, mb, mailsRoot)
	result, err = jm.RunJob(context.Background(), &models.Job{Kind: JobKindNotify}, nil)
	if err != nil || !strings.HasPrefix(result, "sent "+strconv.Itoa(len(groups))+" group(s) to admin@example.test") {
		t.Errorf("notification: %q, %v", result, err)
	}
	if len(srv.Messages()) != 2 {
		t.Errorf("mails sent: %d", len(srv.Messages()))
	}
	// A failing SMTP server makes the job fail with the SMTP error.
	if err := SaveNotificationSettings(db, key, &NotificationInput{SMTPPort: num(closedPort(t))}); err != nil {
		t.Fatal(err)
	}
	if _, err := jm.RunJob(context.Background(), &models.Job{Kind: JobKindNotify}, nil); err == nil || !strings.Contains(err.Error(), "connect to") {
		t.Errorf("unreachable server: %v", err)
	}
	// The inline runner (CLI without a server) takes the notify kind too.
	inline, err := jm.RunJobInline(context.Background(), JobKindNotify, 0, NotifyTestTarget("x@example.test"), RequestedByCLI, nil)
	if err != nil || inline == nil || inline.Status != JobStatusError {
		t.Errorf("inline test job: %+v %v", inline, err)
	}
}

func TestSchedulerQueuesNotifyJob(t *testing.T) {
	db := newTestDB(t)
	if err := SaveCheckTimes(db, []string{}); err != nil {
		t.Fatal(err)
	}
	on := true
	str := func(v string) *string { return &v }
	admin, err := CreateUserFrom(db, NewUser{Username: "admin", Email: "admin@example.test", Password: "password123", Role: RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveNotificationSettings(db, nil, &NotificationInput{
		Enabled: &on, Times: &[]string{"09:00"}, SMTPHost: str("smtp.example.test"), SMTPFrom: str("mailcare@example.test"),
		UserIDs: &[]int64{admin.ID},
	}); err != nil {
		t.Fatal(err)
	}
	jm := NewJobManager(db, nil, t.TempDir(), t.TempDir(), nil)
	s := NewScheduler(db, jm)
	clock := localDate(2026, 9, 17, 8, 59, 50)
	s.now = func() time.Time { return clock }
	s.lastTick = clock
	// Only the notify jobs are counted (the daily cleanup may be queued on
	// the first tick as well).
	clock = localDate(2026, 9, 17, 9, 0, 10)
	s.Tick()
	jobs := activeJobsOfKind(t, db, JobKindNotify)
	if len(jobs) != 1 || jobs[0].MailboxID.Valid || jobs[0].Target != "" || jobs[0].RequestedBy != RequestedByScheduler {
		t.Fatalf("after 09:00: %d jobs %+v", len(jobs), jobs)
	}
	clock = localDate(2026, 9, 17, 9, 0, 40)
	s.Tick()
	if jobs = activeJobsOfKind(t, db, JobKindNotify); len(jobs) != 1 {
		t.Errorf("notify re-queued within the minute: %d", len(jobs))
	}
	// Sent today: tomorrow with interval 2 does not fire, the day after does.
	if err := models.FinishJob(db, jobs[0].ID, "sent", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetNotifyLastSent(db, clock); err != nil {
		t.Fatal(err)
	}
	two := 2
	if err := SaveNotificationSettings(db, nil, &NotificationInput{IntervalDays: &two}); err != nil {
		t.Fatal(err)
	}
	clock = localDate(2026, 9, 18, 8, 59, 50)
	s.Tick()
	clock = localDate(2026, 9, 18, 9, 0, 10)
	s.Tick()
	if jobs = activeJobsOfKind(t, db, JobKindNotify); len(jobs) != 0 {
		t.Errorf("fired before the interval elapsed: %d", len(jobs))
	}
	clock = localDate(2026, 9, 19, 8, 59, 50)
	s.Tick()
	clock = localDate(2026, 9, 19, 9, 0, 10)
	s.Tick()
	if jobs = activeJobsOfKind(t, db, JobKindNotify); len(jobs) != 1 {
		t.Errorf("not fired after the interval: %d", len(jobs))
	}
	// Disabling stops the scheduler at once.
	off := false
	if err := SaveNotificationSettings(db, nil, &NotificationInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	_ = models.FinishJob(db, jobs[0].ID, "sent", "")
	clock = localDate(2026, 9, 25, 8, 59, 50)
	s.Tick()
	clock = localDate(2026, 9, 25, 9, 0, 10)
	s.Tick()
	if jobs = activeJobsOfKind(t, db, JobKindNotify); len(jobs) != 0 {
		t.Errorf("fired while disabled: %d", len(jobs))
	}
}

func TestSMTPConfigForTestGuardsTheStoredPassword(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }
	// Nothing stored: no password is required, overrides are applied.
	cfg, err := SMTPConfigForTest(db, key, &SMTPTestInput{Host: str("mail.example.test"), From: str("MailCare@Example.test")})
	if err != nil || cfg.Host != "mail.example.test" || cfg.From != "mailcare@example.test" || cfg.Password != "" || cfg.Port != DefaultSMTPPort {
		t.Fatalf("without stored settings: %+v %v", cfg, err)
	}
	if err := SaveNotificationSettings(db, key, &NotificationInput{SMTPHost: str("mail.example.test"), SMTPPort: num(2525), SMTPSecurity: str("starttls"),
		SMTPUsername: str("user"), SMTPPassword: str("s3cret"), SMTPFrom: str("mailcare@example.test")}); err != nil {
		t.Fatal(err)
	}
	// The saved server (nil input, or the saved values repeated) gets the
	// stored password; another sender address does not matter.
	for _, in := range []*SMTPTestInput{nil, {}, {Host: str(" mail.example.test "), Port: num(2525), Security: str("STARTTLS"), Username: str("user")},
		{From: str("other@example.test")}} {
		cfg, err := SMTPConfigForTest(db, key, in)
		if err != nil || cfg.Password != "s3cret" || cfg.Host != "mail.example.test" || cfg.Port != 2525 || cfg.Security != "starttls" || cfg.Username != "user" {
			t.Errorf("saved server %+v: %+v %v", in, cfg, err)
		}
	}
	// Any change of host, port, security or username without a password is
	// refused, so the stored password cannot be sent elsewhere.
	for _, in := range []*SMTPTestInput{
		{Host: str("evil.example.test")}, {Port: num(25)}, {Security: str("none")}, {Username: str("other")}, {Username: str("other"), Password: str("")},
	} {
		if _, err := SMTPConfigForTest(db, key, in); !errors.Is(err, ErrSMTPPasswordRequired) {
			t.Errorf("%+v: %v, want ErrSMTPPasswordRequired", in, err)
		}
	}
	// With a password in the request the change is allowed and that password is
	// used, never the stored one.
	cfg, err = SMTPConfigForTest(db, key, &SMTPTestInput{Host: str("other.example.test"), Password: str("theirs")})
	if err != nil || cfg.Host != "other.example.test" || cfg.Password != "theirs" || cfg.Port != 2525 || cfg.Username != "user" {
		t.Errorf("password of the request: %+v %v", cfg, err)
	}
	// Invalid overrides are rejected like the settings update.
	for _, in := range []*SMTPTestInput{{Port: num(0), Password: str("x")}, {Security: str("tls"), Password: str("x")}, {From: str("nope"), Password: str("x")}} {
		if _, err := SMTPConfigForTest(db, key, in); err == nil || errors.Is(err, ErrSMTPPasswordRequired) {
			t.Errorf("%+v: %v, want a validation error", in, err)
		}
	}
}

// The SMTP password belongs to the username: a username needs a password
// (typed or stored); an empty password field keeps the stored one; clearing
// the username removes the stored password without asking for it; another
// non-empty username, host, port or security still needs the password. The
// test mail follows the same rules.
func TestSMTPAuthenticationRules(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }
	missing := func(err error) bool {
		m, ok := wording.As(err)
		return ok && m.Key == "validation.connection.passwordRequired"
	}
	if err := SaveNotificationSettings(db, key, &NotificationInput{SMTPHost: str("mail.example.test"), SMTPUsername: str("user")}); !missing(err) {
		t.Fatalf("username without a password: %v", err)
	}
	if s, _ := ResolveNotificationSettings(db, nil); s.SMTP.Host != "" || s.SMTP.Username != "" {
		t.Errorf("a refused update saved something: %+v", s.SMTP)
	}
	if _, err := SMTPConfigForTest(db, key, &SMTPTestInput{Host: str("mail.example.test"), Username: str("user"), From: str("a@example.test")}); !missing(err) {
		t.Errorf("test mail with a username and no password: %v", err)
	}
	if err := SaveNotificationSettings(db, key, &NotificationInput{SMTPHost: str("mail.example.test"), SMTPUsername: str("user"),
		SMTPPassword: str("s3cret")}); err != nil {
		t.Fatal(err)
	}
	// The empty field keeps the stored password; removing it while the
	// username stays is refused.
	if err := SaveNotificationSettings(db, key, &NotificationInput{SMTPUsername: str("user"), SMTPPassword: str("")}); err != nil {
		t.Errorf("unchanged username, empty password field: %v", err)
	}
	if err := SaveNotificationSettings(db, key, &NotificationInput{ClearSMTPPassword: true}); !missing(err) {
		t.Errorf("password removed while the username stays: %v", err)
	}
	for _, in := range []*NotificationInput{{SMTPUsername: str("other")}, {SMTPHost: str("other.example.test")}, {SMTPPort: num(25)}} {
		if err := SaveNotificationSettings(db, key, in); !errors.Is(err, ErrSMTPPasswordRequired) {
			t.Errorf("%+v without the password: %v", in, err)
		}
	}
	if s, _ := ResolveNotificationSettings(db, key); s.SMTP.Password != "s3cret" || s.SMTP.Username != "user" {
		t.Errorf("stored password or username lost: %+v", s.SMTP)
	}
	// No username: no password needed (also with another host), the stored
	// one is removed and a typed one is not kept; the test mail uses none.
	if cfg, err := SMTPConfigForTest(db, key, &SMTPTestInput{Host: str("other.example.test"), Username: str("")}); err != nil || cfg.Password != "" {
		t.Errorf("test mail without a username: %+v %v", cfg, err)
	}
	if err := SaveNotificationSettings(db, key, &NotificationInput{SMTPHost: str("other.example.test"), SMTPUsername: str(""),
		SMTPPassword: str("ignored")}); err != nil {
		t.Fatalf("username cleared: %v", err)
	}
	if s, _ := ResolveNotificationSettings(db, key); s.SMTPPasswordSet || s.SMTP.Username != "" || s.SMTP.Host != "other.example.test" {
		t.Errorf("after the username was cleared: %+v (password set %v)", s.SMTP, s.SMTPPasswordSet)
	}
}

func TestNotificationsNeedSMTP(t *testing.T) {
	db := newTestDB(t)
	on, off := true, false
	str := func(v string) *string { return &v }
	refused := func(err error) bool {
		m, ok := wording.As(err)
		return ok && m.Key == "validation.notification.smtpRequired"
	}
	if err := SaveNotificationSettings(db, nil, &NotificationInput{Enabled: &on}); !refused(err) {
		t.Fatalf("on without SMTP: %v", err)
	}
	if err := SaveNotificationSettings(db, nil, &NotificationInput{Enabled: &on, SMTPHost: str("smtp.example.test")}); !refused(err) {
		t.Fatalf("on without sender address: %v", err)
	}
	if err := SaveNotificationSettings(db, nil, &NotificationInput{
		Enabled: &on, SMTPHost: str("smtp.example.test"), SMTPFrom: str("mailcare@example.test"),
	}); err != nil {
		t.Fatalf("on with SMTP: %v", err)
	}
	// Clearing the host while notifications stay on is refused; switching off
	// in the same update is accepted.
	if err := SaveNotificationSettings(db, nil, &NotificationInput{SMTPHost: str("")}); !refused(err) {
		t.Fatalf("clearing the host while on: %v", err)
	}
	if err := SaveNotificationSettings(db, nil, &NotificationInput{Enabled: &off, SMTPHost: str("")}); err != nil {
		t.Fatalf("off and clearing the host: %v", err)
	}
	s, err := ResolveNotificationSettings(db, nil)
	if err != nil || s.Enabled || s.SMTP.Host != "" {
		t.Fatalf("saved: %+v %v", s, err)
	}
}

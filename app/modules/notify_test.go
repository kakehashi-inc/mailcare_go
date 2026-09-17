package modules

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

// newNotifyTestDB is newTestDB with the mail templates wired in.
func newNotifyTestDB(t *testing.T) *sql.DB {
	t.Helper()
	TemplatesFS = os.DirFS("../..")
	return newTestDB(t)
}

func TestNotifyDueAndNextNotifyAt(t *testing.T) {
	s := &NotificationSettings{Enabled: true, Time: "09:00", IntervalDays: 1}
	tick := func(last, now time.Time, fired string) (string, bool) { return NotifyDue(last, now, s, fired) }

	// Disabled: never due.
	s.Enabled = false
	if _, due := tick(localDate(2026, 9, 17, 8, 59, 50), localDate(2026, 9, 17, 9, 0, 10), ""); due {
		t.Errorf("due while disabled")
	}
	s.Enabled = true
	// Before the time: not due. First arrival with no last sent: due.
	if _, due := tick(localDate(2026, 9, 17, 8, 59, 20), localDate(2026, 9, 17, 8, 59, 50), ""); due {
		t.Errorf("due before the notify time")
	}
	key, due := tick(localDate(2026, 9, 17, 8, 59, 50), localDate(2026, 9, 17, 9, 0, 10), "")
	if !due || key != "2026-09-17 09:00" {
		t.Errorf("first arrival: %q %v", key, due)
	}
	// The same minute never fires twice, even when the ticks straddle it.
	if _, due := tick(localDate(2026, 9, 17, 8, 59, 50), localDate(2026, 9, 17, 9, 0, 40), key); due {
		t.Errorf("fired twice within the minute")
	}
	// A restart later that day does not fire retroactively (the occurrence
	// lies before the first tick).
	if _, due := tick(localDate(2026, 9, 17, 12, 0, 0), localDate(2026, 9, 17, 12, 0, 30), ""); due {
		t.Errorf("fired retroactively")
	}
	// Interval 1: sent today at 09:00 -> tomorrow is due, a later tick today is not.
	s.LastSentAt = sql.NullTime{Time: localDate(2026, 9, 17, 9, 0, 5).UTC(), Valid: true}
	if _, due := tick(localDate(2026, 9, 17, 8, 59, 50), localDate(2026, 9, 17, 9, 0, 10), ""); due {
		t.Errorf("due on the day it was sent")
	}
	if key, due := tick(localDate(2026, 9, 18, 8, 59, 50), localDate(2026, 9, 18, 9, 0, 10), ""); !due || key != "2026-09-18 09:00" {
		t.Errorf("next day with interval 1: %q %v", key, due)
	}
	// Interval 3: days 1 and 2 are skipped, day 3 fires. A mail sent late
	// in the evening still counts by its calendar day.
	s.IntervalDays = 3
	s.LastSentAt = sql.NullTime{Time: localDate(2026, 9, 17, 23, 30, 0).UTC(), Valid: true}
	for _, day := range []int{18, 19} {
		if _, due := tick(localDate(2026, 9, day, 8, 59, 50), localDate(2026, 9, day, 9, 0, 10), ""); due {
			t.Errorf("due on day %d with interval 3", day)
		}
	}
	if _, due := tick(localDate(2026, 9, 20, 8, 59, 50), localDate(2026, 9, 20, 9, 0, 10), ""); !due {
		t.Errorf("not due on day 3 with interval 3")
	}
	// A tick spanning midnight still catches yesterday's occurrence.
	s.Time = "23:59"
	s.LastSentAt = sql.NullTime{}
	if key, due := tick(localDate(2026, 9, 20, 23, 58, 50), localDate(2026, 9, 21, 0, 0, 10), ""); !due || key != "2026-09-20 23:59" {
		t.Errorf("midnight span: %q %v", key, due)
	}

	// NextNotifyAt.
	s = &NotificationSettings{Enabled: false, Time: "09:00", IntervalDays: 1}
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

func TestNotificationSettingsSaveAndResolve(t *testing.T) {
	db := newNotifyTestDB(t)
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
	if s.SMTP.Port != DefaultSMTPPort || s.SMTP.Security != DefaultSMTPSecurity || s.Enabled || s.Time != DefaultNotifyTime ||
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
		"time":     {Time: str("25:00")},
		"times":    {Time: str("09:00,10:00")},
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
		PublicBaseURL: str("https://mailcare.example.test/"), Enabled: &on, Time: str("7:30"), IntervalDays: num(3),
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
		s.PublicBaseURL != "https://mailcare.example.test" || !s.Enabled || s.Time != "07:30" || s.IntervalDays != 3 ||
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
	// "settings set smtp_password \"\"" removes the stored password.
	if _, err := ApplySetting(db, settingSMTPPassword, "", nil); err != nil {
		t.Fatal(err)
	}
	if s, _ = ResolveNotificationSettings(db, key); s.SMTPPasswordSet {
		t.Errorf("password not cleared")
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
	db := newNotifyTestDB(t)
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
		{SettingSMTPUsername, "bounce", "bounce"},
		{"smtp_password", "s3cret", "(encrypted)"},
		{SettingSMTPFrom, "MailCare@Example.test", "mailcare@example.test"},
		{SettingPublicBaseURL, "https://mailcare.example.test/", "https://mailcare.example.test"},
		{SettingNotifyEnabled, "yes", "true"},
		{SettingNotifyTime, "7:05", "07:05"},
		{SettingNotifyInterval, "2", "2"},
		{SettingNotifyUserIDs, "1, 1", "1"},
	} {
		got, err := ApplySetting(db, ok[0], ok[1], loadKey)
		if err != nil || got != ok[2] {
			t.Errorf("%s=%s: %q, %v (want %q)", ok[0], ok[1], got, err, ok[2])
		}
	}
	key, _ := LoadSecretKey(db)
	s, err := ResolveNotificationSettings(db, key)
	if err != nil {
		t.Fatal(err)
	}
	if s.SMTP.Host != "smtp.example.test" || s.SMTP.Port != 465 || s.SMTP.Security != IMAPSecuritySSL || s.SMTP.Password != "s3cret" ||
		s.SMTP.From != "mailcare@example.test" || s.PublicBaseURL != "https://mailcare.example.test" || !s.Enabled ||
		s.Time != "07:05" || s.IntervalDays != 2 || len(s.UserIDs) != 1 || s.UserIDs[0] != admin.ID {
		t.Errorf("resolved after CLI: %+v", s)
	}
	// With a password stored, a connection key cannot change on its own
	// (the CLI sets one key per command, so the operator removes the
	// password first); other keys and the unchanged value still work.
	for _, change := range [][2]string{{SettingSMTPHost, "other.example.test"}, {SettingSMTPPort, "25"}, {SettingSMTPSecurity, "none"}, {SettingSMTPUsername, "other"}} {
		_, err := ApplySetting(db, change[0], change[1], loadKey)
		if !isArg(err) || !strings.Contains(err.Error(), ErrSMTPPasswordRequired.Error()) || !strings.Contains(err.Error(), `settings set smtp_password ""`) {
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
	// An empty value restores the default; for the password it clears the stored one.
	if _, err := ApplySetting(db, "smtp_password", "", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := ApplySetting(db, SettingSMTPHost, "other.example.test", loadKey); err != nil || got != "other.example.test" {
		t.Errorf("host change once the password is removed: %q %v", got, err)
	}
	if _, err := ApplySetting(db, SettingNotifyInterval, "", nil); err != nil {
		t.Fatal(err)
	}
	if s, _ = ResolveNotificationSettings(db, key); s.SMTPPasswordSet || s.IntervalDays != DefaultNotifyIntervalDays {
		t.Errorf("defaults not restored: %+v", s)
	}
	// The password needs the master key.
	if _, err := ApplySetting(db, "smtp_password", "x", nil); err == nil {
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
		if i == 0 {
			summary = ""
		}
		markdown := "# 原因の分析\n\nfirst paragraph of " + g.GroupKey + "\ncontinued\n\nsecond paragraph\n"
		if err := models.CompleteAgentReport(idx, r.ID, summary, ResponsibleSender, "high", markdown); err != nil {
			t.Fatal(err)
		}
	}
	return groups
}

func TestSendNotificationEndToEnd(t *testing.T) {
	db := newNotifyTestDB(t)
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
	carol, _ := CreateUserFrom(db, NewUser{Username: "carol", Email: "carol@example.test", Password: "password123", Role: RoleUser})
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
	if !res.Sent || res.Groups != len(groups) || strings.Join(res.Recipients, ",") != "admin@example.test,carol@example.test" {
		t.Errorf("result %+v", res)
	}
	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("mails sent: %d", len(msgs))
	}
	m := msgs[0]
	if strings.Join(m.To, ",") != "admin@example.test,carol@example.test" || m.From != "mailcare@example.test" {
		t.Errorf("envelope %+v", m)
	}
	subject := decodeSubject(t, m.Data)
	if !strings.HasPrefix(subject, NotifyMailSubjectPrefix) || !strings.Contains(subject, " "+strconv.Itoa(len(groups))+" ") || !strings.Contains(subject, "(2026-09-17)") {
		t.Errorf("subject = %q", subject)
	}
	if subject != res.Subject {
		t.Errorf("result subject %q != mail subject %q", res.Subject, subject)
	}
	body := m.Data[strings.Index(m.Data, "\r\n\r\n")+4:]
	if !strings.Contains(body, mb.Address) || strings.Contains(body, "empty@example.test") {
		t.Errorf("mailbox headings wrong:\n%s", body)
	}
	labels, err := loadCategoryLabels()
	if err != nil {
		t.Fatal(err)
	}
	for i, g := range groups {
		url := GroupURL("http://localhost:9790", mb.ID, g.GroupKey)
		if !strings.Contains(body, url) {
			t.Errorf("body lacks the URL %s:\n%s", url, body)
		}
		if label := labels[g.Category]; label == "" || !strings.Contains(body, label+": "+g.UnitValue) {
			t.Errorf("body lacks the category block %q for %s:\n%s", label, g.Category, body)
		}
		want := "summary of " + g.GroupKey
		if i == 0 {
			want = "first paragraph of " + g.GroupKey + " continued"
		}
		if !strings.Contains(body, want) {
			t.Errorf("body lacks the summary %q:\n%s", want, body)
		}
	}
	if strings.Count(body, "URL: ") != len(groups) {
		t.Errorf("%d URL lines, want %d:\n%s", strings.Count(body, "URL: "), len(groups), body)
	}
	if strings.Contains(m.Data, "<no value>") || strings.Contains(body, "{{") {
		t.Errorf("template left placeholders:\n%s", body)
	}
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
		if err := models.SetGroupState(idx, g.GroupKey, GroupStateResolved); err != nil {
			t.Fatal(err)
		}
	}
	idx.Close()
	expectSkip(SkipNoActionableGroups)
	// The public base URL is used for links when set.
	if err := SaveNotificationSettings(db, key, &NotificationInput{PublicBaseURL: str("https://mailcare.example.test")}); err != nil {
		t.Fatal(err)
	}
	s, _ = ResolveNotificationSettings(db, nil)
	n, err := BuildNotification(ctx, db, mailsRoot, EffectiveBaseURL(db, s), now)
	if err != nil || n.Total != 0 || !strings.Contains(n.Body, "https://mailcare.example.test/settings/notifications") {
		t.Errorf("build with base URL: total %d, err %v\n%s", n.Total, err, n.Body)
	}
}

func TestNotifyJobRunsThroughTheManager(t *testing.T) {
	db := newNotifyTestDB(t)
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
	db := newNotifyTestDB(t)
	if err := SaveCheckTimes(db, []string{}); err != nil {
		t.Fatal(err)
	}
	on := true
	str := func(v string) *string { return &v }
	if err := SaveNotificationSettings(db, nil, &NotificationInput{Enabled: &on, Time: str("09:00")}); err != nil {
		t.Fatal(err)
	}
	jm := NewJobManager(db, nil, t.TempDir(), t.TempDir(), nil)
	s := NewScheduler(db, jm)
	clock := localDate(2026, 9, 17, 8, 59, 50)
	s.now = func() time.Time { return clock }
	s.lastTick = clock
	// Only the notify jobs are counted (the daily cleanup is queued on the
	// first tick of a day as well).
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
	db := newNotifyTestDB(t)
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
	// With a password of its own the change is allowed and that password is
	// used, never the stored one.
	cfg, err = SMTPConfigForTest(db, key, &SMTPTestInput{Host: str("other.example.test"), Password: str("theirs")})
	if err != nil || cfg.Host != "other.example.test" || cfg.Password != "theirs" || cfg.Port != 2525 || cfg.Username != "user" {
		t.Errorf("own password: %+v %v", cfg, err)
	}
	// Invalid overrides are rejected like the settings update.
	for _, in := range []*SMTPTestInput{{Port: num(0), Password: str("x")}, {Security: str("tls"), Password: str("x")}, {From: str("nope"), Password: str("x")}} {
		if _, err := SMTPConfigForTest(db, key, in); err == nil || errors.Is(err, ErrSMTPPasswordRequired) {
			t.Errorf("%+v: %v, want a validation error", in, err)
		}
	}
}

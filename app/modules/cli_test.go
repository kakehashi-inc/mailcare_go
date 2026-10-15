package modules

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"mailcare/app/models"
)

// pipeStdin replaces os.Stdin (so readSecret sees no terminal) and the shared
// line reader with the given input for the rest of the test.
func pipeStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	w.Close()
	origStdin, origReader := os.Stdin, stdinReader
	os.Stdin, stdinReader = r, bufio.NewReader(r)
	t.Cleanup(func() {
		os.Stdin, stdinReader = origStdin, origReader
		r.Close()
	})
}

// TestPromptPasswordPipedInput checks the password prompt on piped input:
// two matching lines give the password, and a missing first line, a missing
// confirmation line and a mismatch are separate argument errors.
func TestPromptPasswordPipedInput(t *testing.T) {
	for _, tc := range []struct{ input, want, message string }{
		{"secret\nsecret\n", "secret", ""},
		{"secret\nsecret", "secret", ""},
		{"", "", "no password given"},
		{"secret\n", "", "the password must be entered twice (the confirmation line is missing)"},
		{"secret\nother\n", "", "the passwords do not match"},
	} {
		pipeStdin(t, tc.input)
		var got string
		var err error
		captureStdout(t, func() { got, err = promptPassword("Password") })
		if tc.message == "" {
			if err != nil || got != tc.want {
				t.Errorf("input %q: %q, %v; want %q", tc.input, got, err, tc.want)
			}
			continue
		}
		var exitErr *ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != ExitArgument || exitErr.Message != tc.message {
			t.Errorf("input %q: %v; want argument error %q", tc.input, err, tc.message)
		}
	}
	// The lines are read from the shared reader, so the prompt does not
	// swallow the input that follows the password.
	pipeStdin(t, "secret\nsecret\ny\n")
	captureStdout(t, func() {
		if got, err := promptPassword("Password"); err != nil || got != "secret" {
			t.Errorf("password before the confirmation: %q, %v", got, err)
		}
		if !confirm("Continue? ") {
			t.Error("the line after the password was lost")
		}
	})
}

// TestSettingsSetOwnSendingAndDNSCheck checks "settings set" for the own
// sending lists and the DNS check settings: comma-separated lists, the word
// none for an empty resolver list, "" restoring the default and the rule
// that the enabled verification keeps a resolver, checked against the
// saved values of the other keys (restoring a default included).
func TestSettingsSetOwnSendingAndDNSCheck(t *testing.T) {
	db := newTestDB(t)
	isArg := func(err error) bool {
		var exitErr *ExitError
		return errors.As(err, &exitErr) && exitErr.Code == ExitArgument
	}
	for _, ok := range [][3]string{
		{SettingOwnSendingIPv4, "203.0.113.5, 198.51.100.7/24,203.0.113.5", "203.0.113.5, 198.51.100.0/24"},
		{SettingOwnSendingIPv6, "2001:DB8::1", "2001:db8::1"},
		{SettingOwnSendingHosts, "MX1.Example.", "mx1.example"},
		{SettingDNSCheckResolvers, "9.9.9.9, 2606:4700:4700::1111", "9.9.9.9, 2606:4700:4700::1111"},
		{SettingDNSCheckEnabled, "off", "false"},
		{SettingDNSCheckSystemResolverOnly, "yes", "true"},
	} {
		got, err := ApplySetting(db, ok[0], ok[1], nil)
		if err != nil || got != ok[2] {
			t.Errorf("%s=%s: %q, %v (want %q)", ok[0], ok[1], got, err, ok[2])
		}
	}
	if saved := models.GetSetting(db, SettingOwnSendingIPv4); saved != "203.0.113.5\n198.51.100.0/24" {
		t.Errorf("stored own_sending_ipv4 %q", saved)
	}
	if saved := models.GetSetting(db, SettingDNSCheckResolvers); saved != "9.9.9.9\n2606:4700:4700::1111" {
		t.Errorf("stored dns_check_resolvers %q", saved)
	}
	for _, bad := range [][2]string{
		{SettingOwnSendingIPv4, "203.0.113.5,2001:db8::1"},
		{SettingOwnSendingIPv6, "203.0.113.5"},
		{SettingOwnSendingHosts, "203.0.113.5"},
		{SettingOwnSendingHosts, "mail_1.example"},
		{SettingDNSCheckResolvers, "1.1.1.1:53"},
		{SettingDNSCheckResolvers, "127.0.0.1"},
		{SettingDNSCheckEnabled, "maybe"},
		{SettingDNSCheckSystemResolverOnly, "2"},
	} {
		if _, err := ApplySetting(db, bad[0], bad[1], nil); !isArg(err) {
			t.Errorf("%s=%s: %v, want an argument error", bad[0], bad[1], err)
		}
	}
	// Only none saves an empty list (distinct from no row), accepted here
	// as only the OS resolver is used; a value of commas is refused.
	commasOnly := "dns_check_resolvers lists no address (use none to save an empty list)"
	if _, err := ApplySetting(db, SettingDNSCheckResolvers, " , ,", nil); !isArg(err) || err.Error() != commasOnly {
		t.Errorf("resolvers of commas only: %v", err)
	}
	if got, err := ApplySetting(db, SettingDNSCheckResolvers, "NONE", nil); err != nil || got != DNSCheckResolversNone {
		t.Fatalf("resolvers none: %q %v", got, err)
	}
	if v, found, _ := models.GetSettingStrict(db, SettingDNSCheckResolvers); !found || v != "" {
		t.Errorf("none stored as %q (%v)", v, found)
	}
	// While the verification is off, leaving the OS resolver with no
	// resolver is accepted (here by restoring the default).
	if _, err := ApplySetting(db, SettingDNSCheckSystemResolverOnly, "", nil); err != nil {
		t.Fatalf("restore system only with the verification off: %v", err)
	}
	// Turning the verification on now (by value or by restoring the
	// default) would leave it without a resolver.
	for _, value := range []string{"on", ""} {
		_, err := ApplySetting(db, SettingDNSCheckEnabled, value, nil)
		if !isArg(err) || !strings.Contains(err.Error(), SettingDNSCheckResolvers) {
			t.Errorf("dns_check_enabled=%q with no resolver: %v", value, err)
		}
	}
	if ResolveDNSCheckEnabled(db) {
		t.Error("a refused change turned the verification on")
	}
	// With the verification on through the OS resolver, leaving the OS
	// resolver is refused the same way.
	if _, err := ApplySetting(db, SettingDNSCheckSystemResolverOnly, "on", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplySetting(db, SettingDNSCheckEnabled, "", nil); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"off", ""} {
		if _, err := ApplySetting(db, SettingDNSCheckSystemResolverOnly, value, nil); !isArg(err) {
			t.Errorf("dns_check_system_resolver_only=%q with no resolver: %v", value, err)
		}
	}
	if !ResolveDNSCheckSystemResolverOnly(db) {
		t.Error("a refused change left the OS resolver")
	}
	// "" restores the default resolvers (the row is removed); none is then
	// refused once the verification uses them.
	if _, err := ApplySetting(db, SettingDNSCheckResolvers, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := models.GetSettingStrict(db, SettingDNSCheckResolvers); found {
		t.Error("restoring the default resolvers kept the row")
	}
	if _, err := ApplySetting(db, SettingDNSCheckSystemResolverOnly, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplySetting(db, SettingDNSCheckResolvers, DNSCheckResolversNone, nil); !isArg(err) {
		t.Errorf("resolvers none with the verification on: %v", err)
	}
	if cfg := ResolveDNSCheck(db); !cfg.Enabled || cfg.SystemOnly || !reflect.DeepEqual(cfg.Resolvers, DefaultDNSCheckResolvers()) {
		t.Errorf("after restoring the defaults: %+v", cfg)
	}
	// With the verification on, a value of commas gets the same message
	// (not the one about a verification without a resolver), and an invalid
	// entry among the commas its own.
	if _, err := ApplySetting(db, SettingDNSCheckResolvers, ", ,", nil); !isArg(err) || err.Error() != commasOnly {
		t.Errorf("resolvers of commas only with the verification on: %v", err)
	}
	if _, err := ApplySetting(db, SettingDNSCheckResolvers, ", 127.0.0.1,", nil); !isArg(err) || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("an invalid resolver among commas: %v", err)
	}
	if cfg := ResolveDNSCheck(db); !reflect.DeepEqual(cfg.Resolvers, DefaultDNSCheckResolvers()) {
		t.Errorf("a refused value was saved: %+v", cfg)
	}
	// "" clears an own sending list.
	if _, err := ApplySetting(db, SettingOwnSendingHosts, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := models.GetSettingStrict(db, SettingOwnSendingHosts); found {
		t.Error("own_sending_hosts still stored")
	}
	// The SMTP hosts of the mailboxes are shown by "settings show" but set
	// on the mailboxes (mailbox update --smtp-host).
	if _, err := ApplySetting(db, "own_sending_mailbox_hosts", "mx1.example", nil); !isArg(err) || !strings.Contains(err.Error(), `unknown setting "own_sending_mailbox_hosts"`) {
		t.Errorf("own_sending_mailbox_hosts: %v, want the unknown setting error", err)
	}
	// The command itself answers a refused change with the argument exit
	// code.
	none := DNSCheckResolversNone
	var err error
	captureStdout(t, func() { err = (&SettingsSetCmd{Key: SettingDNSCheckResolvers, Value: &none}).Run() })
	if !isArg(err) {
		t.Errorf("settings set dns_check_resolvers none: %v", err)
	}
}

// TestSettingsShowOwnSendingAndDNSCheck checks how "settings show" prints
// the own sending lists, the SMTP hosts of the mailboxes (shown, not set
// there) and the DNS check settings.
func TestSettingsShowOwnSendingAndDNSCheck(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	show := func(asJSON bool) string {
		var err error
		out := captureStdout(t, func() { err = (&SettingsShowCmd{JSON: asJSON}).Run() })
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(show(true)), &values); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		SettingOwnSendingIPv4: []any{}, SettingOwnSendingIPv6: []any{}, SettingOwnSendingHosts: []any{}, "own_sending_mailbox_hosts": []any{},
		"own_sending_mailboxes_without_host": []any{},
		SettingDNSCheckEnabled:               true, SettingDNSCheckResolvers: []any{"1.1.1.1", "8.8.8.8", "9.9.9.9"},
		SettingDNSCheckSystemResolverOnly: false,
	}
	for key, v := range want {
		if !reflect.DeepEqual(values[key], v) {
			t.Errorf("JSON %s = %#v, want %#v", key, values[key], v)
		}
	}
	text := show(false)
	for _, line := range []string{"own_sending_ipv4:", "(not set)", "own_sending_mailbox_hosts:", "(none: set with mailbox update --smtp-host)",
		"dns_check_resolvers:", "1.1.1.1, 8.8.8.8, 9.9.9.9"} {
		if !strings.Contains(text, line) {
			t.Errorf("text output lacks %q:\n%s", line, text)
		}
	}
	smtpHost := "MX1.Example."
	if _, err := CreateMailbox(db, key, &MailboxInput{Address: "ops@example.test", ImapHost: "imap.example.test",
		ImapUsername: "u", ImapPassword: "p", SmtpHost: &smtpHost}); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateMailbox(db, key, &MailboxInput{Address: "user1@example.test", ImapHost: "imap.example.test",
		ImapUsername: "u", ImapPassword: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(show(true)), &values); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(values["own_sending_mailboxes_without_host"], []any{"user1@example.test"}) {
		t.Errorf("JSON own_sending_mailboxes_without_host = %v, want the address without a host", values["own_sending_mailboxes_without_host"])
	}
	if text := show(false); !strings.Contains(text, "mailboxes without a host name:  user1@example.test (backscatter is not judged") {
		t.Errorf("text output lacks the address without a host:\n%s", text)
	}
	for _, kv := range [][2]string{
		{SettingOwnSendingHosts, "relay.example"}, {SettingDNSCheckSystemResolverOnly, "on"},
		{SettingDNSCheckResolvers, DNSCheckResolversNone},
	} {
		if _, err := ApplySetting(db, kv[0], kv[1], nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := json.Unmarshal([]byte(show(true)), &values); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(values[SettingOwnSendingHosts], []any{"relay.example"}) ||
		!reflect.DeepEqual(values["own_sending_mailbox_hosts"], []any{"mx1.example"}) ||
		!reflect.DeepEqual(values["own_sending_mailboxes_without_host"], []any{}) ||
		!reflect.DeepEqual(values[SettingDNSCheckResolvers], []any{}) || values[SettingDNSCheckSystemResolverOnly] != true {
		t.Errorf("JSON after the changes: %v %v %v %v", values[SettingOwnSendingHosts], values["own_sending_mailbox_hosts"],
			values[SettingDNSCheckResolvers], values[SettingDNSCheckSystemResolverOnly])
	}
	text = show(false)
	if !strings.Contains(text, "none (not used: only the OS resolver is queried)") || !strings.Contains(text, "relay.example") ||
		!strings.Contains(text, "own_sending_mailbox_hosts:      mx1.example") {
		t.Errorf("text output after the changes:\n%s", text)
	}
}

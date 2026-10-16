package modules

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mailcare/app/models"
	"mailcare/app/modules/wording"
)

func TestParseCheckTimes(t *testing.T) {
	got, err := ParseCheckTimes([]string{"18:30", "6:00,06:00", " 12:05 "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"06:00", "12:05", "18:30"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, bad := range []string{"24:00", "6:5", "abc", "12:60", "1200", "-1:00"} {
		if _, err := ParseCheckTimes([]string{bad}); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
	empty, err := ParseCheckTimes(nil)
	if err != nil || len(empty) != 0 || empty == nil {
		t.Errorf("empty input: got %v, %v", empty, err)
	}
}

func TestAgentModelClearedOnProviderChange(t *testing.T) {
	db := newTestDB(t)
	if err := SetAgentModel(db, "gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	// Setting the same provider keeps the model.
	if err := SetAgentProvider(db, DefaultAgentProvider); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAgentModel(db); got != "gpt-5.5" {
		t.Errorf("same provider: model %q", got)
	}
	// Another provider clears it (the name is not validated here).
	if err := SetAgentProvider(db, "other"); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAgentModel(db); got != "" {
		t.Errorf("provider change kept model %q", got)
	}
	if SetAgentModel(db, "--bad") == nil {
		t.Error("invalid model accepted")
	}
	// A stored value that is not valid is ignored.
	if err := models.SetSetting(db, SettingAgentModel, "bad model"); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAgentModel(db); got != "" {
		t.Errorf("invalid stored model resolved to %q", got)
	}
}

// writeCodexCache points CODEX_HOME at a model cache in which "m1" accepts
// low and high and "m2" accepts medium.
func writeCodexCache(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	cache := `{"models":[
		{"slug":"m1","visibility":"list","priority":1,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},
		{"slug":"m2","visibility":"list","priority":2,"supported_reasoning_levels":[{"effort":"medium"}]}]}`
	if err := os.WriteFile(filepath.Join(home, "models_cache.json"), []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAgentReasoningEffort(t *testing.T) {
	writeCodexCache(t)
	db := newTestDB(t)
	if got := ResolveAgentReasoningEffort(db); got != "" {
		t.Fatalf("default level %q, want unset", got)
	}
	// Model unset: any level a known model accepts.
	if err := SetAgentReasoningEffort(db, "medium"); err != nil {
		t.Fatal(err)
	}
	if SetAgentReasoningEffort(db, "extreme") == nil {
		t.Error("a level no model accepts must be refused")
	}
	// A model that does not accept the saved level is refused; nothing is
	// saved.
	if SetAgentModel(db, "m1") == nil {
		t.Error("m1 does not accept medium: the model change must be refused")
	}
	if got := ResolveAgentModel(db); got != "" {
		t.Errorf("refused model saved: %q", got)
	}
	if err := SetAgentModel(db, "m2"); err != nil {
		t.Fatal(err)
	}
	if SetAgentReasoningEffort(db, "high") == nil {
		t.Error("m2 does not accept high")
	}
	// Changing both at once is checked as a whole.
	model, level := "m1", "high"
	if err := SaveAgentSettings(db, nil, &model, &level); err != nil {
		t.Fatal(err)
	}
	if ResolveAgentModel(db) != "m1" || ResolveAgentReasoningEffort(db) != "high" {
		t.Errorf("combined save: %q %q", ResolveAgentModel(db), ResolveAgentReasoningEffort(db))
	}
	bad := "medium"
	if SaveAgentSettings(db, nil, nil, &bad) == nil || ResolveAgentReasoningEffort(db) != "high" {
		t.Error("a refused combination must save nothing")
	}
	// A model the cache does not list: only the shape is checked.
	other := "custom-model"
	if err := SaveAgentSettings(db, nil, &other, nil); err != nil {
		t.Errorf("unlisted model: %v", err)
	}
	// "" restores the CLI setting.
	if err := SetAgentReasoningEffort(db, ""); err != nil || ResolveAgentReasoningEffort(db) != "" {
		t.Errorf("clear level: %v %q", err, ResolveAgentReasoningEffort(db))
	}
	// A provider change clears the model and the level.
	level = "high"
	model = "m1"
	if err := SaveAgentSettings(db, nil, &model, &level); err != nil {
		t.Fatal(err)
	}
	if err := SetAgentProvider(db, "other"); err != nil {
		t.Fatal(err)
	}
	if ResolveAgentModel(db) != "" || ResolveAgentReasoningEffort(db) != "" {
		t.Error("provider change must clear the model and the level")
	}
	// A stored value of the wrong shape is ignored.
	if err := models.SetSetting(db, SettingAgentReasoningEffort, "High Level"); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAgentReasoningEffort(db); got != "" {
		t.Errorf("invalid stored level resolved to %q", got)
	}
}

func TestRecheckDaysSettings(t *testing.T) {
	db := newTestDB(t)
	days := ResolveRecheckDays(db)
	if days[CategoryIPBlocked] != 14 || days[CategoryAuthFailure] != 3 || days[CategoryUserUnknown] != 7 ||
		len(days) != len(KnownCategories()) {
		t.Errorf("defaults = %v", days)
	}
	if err := SaveRecheckDays(db, map[string]int{CategoryIPBlocked: 21, CategoryAuthFailure: 3}); err != nil {
		t.Fatal(err)
	}
	// Only the values that differ from the defaults are stored.
	if saved := models.GetSetting(db, SettingRecheckDays); saved != "ip_blocked=21" {
		t.Errorf("saved = %q, want ip_blocked=21", saved)
	}
	// Saving other categories keeps the value of the ones not named.
	if err := SaveRecheckDays(db, map[string]int{CategoryUserUnknown: 10}); err != nil {
		t.Fatal(err)
	}
	if days := ResolveRecheckDays(db); days[CategoryIPBlocked] != 21 || days[CategoryUserUnknown] != 10 {
		t.Errorf("after the second save = %v", days)
	}
	for _, bad := range []map[string]int{{CategoryIPBlocked: 0}, {CategoryIPBlocked: 366}, {"nope": 3}} {
		if err := SaveRecheckDays(db, bad); err == nil {
			t.Errorf("%v was accepted", bad)
		}
	}
	if _, err := ParseRecheckDays("ip_blocked=10, auth_failure = 2"); err != nil {
		t.Errorf("parse: %v", err)
	}
	for _, bad := range []string{"ip_blocked", "ip_blocked=x", "nope=3"} {
		if _, err := ParseRecheckDays(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// wordingKey returns the language-file key of a message error ("" for any
// other error).
func wordingKey(err error) string {
	if m, ok := wording.As(err); ok {
		return m.Key
	}
	return ""
}

func TestParseSendingSource(t *testing.T) {
	// Entries are trimmed, blank lines skipped, IPs written canonically,
	// CIDRs masked, one-address CIDRs written as the address and duplicates
	// dropped (the first kept); an input may hold several lines.
	got, err := ParseSendingSourceIPv4([]string{" 203.0.113.5 ", "", "198.51.100.7/24\r\n198.51.100.0/24", "203.0.113.5/32", "192.0.2.1"})
	if err != nil || !reflect.DeepEqual(got, []string{"203.0.113.5", "198.51.100.0/24", "192.0.2.1"}) {
		t.Errorf("IPv4: %v, %v", got, err)
	}
	got, err = ParseSendingSourceIPv6([]string{"2001:DB8:0:0::1", "2001:db8::1/128", "2001:db8:0:0:1::/32", "2001:db8::/32"})
	if err != nil || !reflect.DeepEqual(got, []string{"2001:db8::1", "2001:db8::/32"}) {
		t.Errorf("IPv6: %v, %v", got, err)
	}
	got, err = ParseSendingSourceHosts([]string{"Mail.Example.COM.", "mail.example.com", "mailhost", "xn--bcher-kva.example", "a-b.example", "mx1.example"})
	if err != nil || !reflect.DeepEqual(got, []string{"mail.example.com", "mailhost", "xn--bcher-kva.example", "a-b.example", "mx1.example"}) {
		t.Errorf("hosts: %v, %v", got, err)
	}
	for _, parse := range []func([]string) ([]string, error){ParseSendingSourceIPv4, ParseSendingSourceIPv6, ParseSendingSourceHosts, ParseDNSCheckResolvers} {
		if got, err := parse(nil); err != nil || got == nil || len(got) != 0 {
			t.Errorf("empty input: %#v, %v", got, err)
		}
		if got, err := parse([]string{"", "  ", "\n"}); err != nil || got == nil || len(got) != 0 {
			t.Errorf("blank input: %#v, %v", got, err)
		}
	}

	longLabel := strings.Repeat("a", 64)
	longName := strings.Repeat("abcdefghi.", 25) + "abcd" // 254 characters
	for _, tc := range []struct {
		name, valid string
		parse       func([]string) ([]string, error)
		key         string
		bad         []string
	}{
		{"ipv4", "192.0.2.1", ParseSendingSourceIPv4, "validation.sendingSource.ipv4Invalid", []string{
			"2001:db8::1", "2001:db8::/32", "mail.example.com", "mailhost", "::ffff:203.0.113.5", "::ffff:0:0/96",
			"203.0.113.0/33", "203.0.113.0/-1", "203.0.113.0/", "203.0.113", "203.0.113.256", "010.0.0.1",
			"203.0.113.5:25", "127.0.0.1", "127.0.0.0/8", "0.0.0.0", "0.0.0.0/0", "203.0.113.5/0", "203.0.113.5%eth0",
		}},
		{"ipv6", "2001:db8::1", ParseSendingSourceIPv6, "validation.sendingSource.ipv6Invalid", []string{
			"203.0.113.5", "203.0.113.0/24", "mail.example.com", "::ffff:203.0.113.5", "::ffff:0:0/96", "::ffff:203.0.113.0/120",
			"fe80::1%eth0", "fe80::/10%eth0", "::1", "::1/128", "::", "::/0", "2001:db8::1/0", "2001:db8::/129", "[2001:db8::1]",
			"[2001:db8::1]:25", "2001:db8::g",
		}},
		{"hosts", "mail.example.com", ParseSendingSourceHosts, "validation.sendingSource.hostInvalid", []string{
			"203.0.113.5", "2001:db8::1", "mail.123", "123", "-mail.example.com", "mail-.example.com", "mail_1.example.com",
			"mail..example.com", ".example.com", ".", "example.com..", longLabel + ".example", longName, "localhost",
			"LocalHost.", "mx.localhost", "localhost.localdomain", "mail example.com", "メール.example.jp",
			"Kexample.com", "*.example.com", "mail.example.com:25", "https://mail.example.com",
		}},
	} {
		for _, bad := range tc.bad {
			// The first input holds two lines (the second blank), so the
			// bad entry is on line 3.
			_, err := tc.parse([]string{tc.valid + "\n", bad})
			m, ok := wording.As(err)
			if !ok || m.Key != tc.key || m.Params["line"] != 3 || m.Params["value"] != clipEntry(bad) {
				t.Errorf("%s %q: %v (%+v)", tc.name, bad, err, m)
			}
		}
	}
	// The boundaries of a host name: a 63-character label and 253
	// characters in all.
	if _, err := ParseSendingSourceHosts([]string{strings.Repeat("a", 63) + ".example", longName[:253]}); err != nil {
		t.Errorf("host name at the limits: %v", err)
	}
	// A long entry is clipped in the message.
	_, err = ParseSendingSourceIPv4([]string{strings.Repeat("x", 100)})
	if m, ok := wording.As(err); !ok || m.Params["value"] != strings.Repeat("x", 64)+"..." {
		t.Errorf("clipped value: %v", err)
	}
}

func TestSendingSourceLimit(t *testing.T) {
	entries := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("10.0.%d.%d", i/250, i%250+1)
		}
		return out
	}
	if got, err := ParseSendingSourceIPv4(entries(MaxSendingSourceEntries)); err != nil || len(got) != MaxSendingSourceEntries {
		t.Errorf("%d entries: %d, %v", MaxSendingSourceEntries, len(got), err)
	}
	_, err := ParseSendingSourceIPv4(entries(MaxSendingSourceEntries + 1))
	if m, ok := wording.As(err); !ok || m.Key != "validation.sendingSource.tooMany" || m.Params["max"] != MaxSendingSourceEntries {
		t.Errorf("%d entries: %v", MaxSendingSourceEntries+1, err)
	}
	// The bound counts the entries left after de-duplication.
	if _, err := ParseSendingSourceIPv4(append(entries(MaxSendingSourceEntries), "10.0.0.1")); err != nil {
		t.Errorf("a duplicate over the bound: %v", err)
	}
	hosts := make([]string, MaxSendingSourceEntries+1)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("mx%d.example", i)
	}
	if _, err := ParseSendingSourceHosts(hosts); wordingKey(err) != "validation.sendingSource.tooMany" {
		t.Errorf("hosts over the bound: %v", err)
	}
}

func TestSendingSourcePersistence(t *testing.T) {
	db := newTestDB(t)
	if sources, err := ResolveSendingSources(db); err != nil || sources.IPv4 == nil || sources.IPv6 == nil || sources.Hosts == nil ||
		sources.MailboxHosts == nil || sources.MailboxesWithoutHost == nil ||
		len(sources.IPv4)+len(sources.IPv6)+len(sources.Hosts)+len(sources.MailboxHosts)+len(sources.MailboxesWithoutHost) != 0 {
		t.Errorf("defaults: %#v (err %v)", sources, err)
	}
	if err := SaveSendingSourceIPv4(db, []string{"203.0.113.5", "198.51.100.0/24"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSendingSourceIPv6(db, []string{"2001:db8::/32"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSendingSourceHosts(db, []string{"MX1.example.", "mailhost"}); err != nil {
		t.Fatal(err)
	}
	if saved := models.GetSetting(db, SettingSendingSourceIPv4); saved != "203.0.113.5\n198.51.100.0/24" {
		t.Errorf("stored IPv4 %q", saved)
	}
	want := SendingSourceSettings{IPv4: []string{"203.0.113.5", "198.51.100.0/24"}, IPv6: []string{"2001:db8::/32"},
		Hosts: []string{"mx1.example", "mailhost"}, MailboxHosts: []string{}, MailboxesWithoutHost: []string{}}
	if sources, err := ResolveSendingSources(db); err != nil || !reflect.DeepEqual(sources, want) {
		t.Errorf("resolved %#v (err %v)", sources, err)
	}
	// A refused list saves nothing.
	if err := SaveSendingSourceIPv4(db, []string{"203.0.113.9", "2001:db8::1"}); err == nil {
		t.Error("an IPv6 address was saved in the IPv4 list")
	}
	if got := ResolveSendingSourceIPv4(db); !reflect.DeepEqual(got, want.IPv4) {
		t.Errorf("after a refused save: %v", got)
	}
	// An empty list removes the setting.
	if err := SaveSendingSourceHosts(db, []string{"", " "}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := models.GetSettingStrict(db, SettingSendingSourceHosts); found {
		t.Error("an empty host list is still stored")
	}
	// A saved entry that is not valid is skipped, the others are kept.
	if err := models.SetSetting(db, SettingSendingSourceIPv6, "2001:db8::1\nbogus\n::1\n2001:DB8::1"); err != nil {
		t.Fatal(err)
	}
	if got := ResolveSendingSourceIPv6(db); !reflect.DeepEqual(got, []string{"2001:db8::1"}) {
		t.Errorf("lenient resolve: %v", got)
	}
}

func TestDNSCheckResolvers(t *testing.T) {
	got, err := ParseDNSCheckResolvers([]string{" 9.9.9.9", "2606:4700:4700:0::1111", "9.9.9.9", "1.1.1.1"})
	if err != nil || !reflect.DeepEqual(got, []string{"9.9.9.9", "2606:4700:4700::1111", "1.1.1.1"}) {
		t.Errorf("parse: %v, %v", got, err)
	}
	for _, bad := range []string{
		"1.1.1.0/24", "1.1.1.1/32", "1.1.1.1:53", "[2606:4700:4700::1111]:53", "127.0.0.1", "::1", "0.0.0.0", "::",
		"::ffff:1.1.1.1", "fe80::1%eth0", "dns.google", "1.1.1",
	} {
		_, err := ParseDNSCheckResolvers([]string{"8.8.8.8", bad})
		m, ok := wording.As(err)
		if !ok || m.Key != "validation.dnsCheck.resolverInvalid" || m.Params["line"] != 2 || m.Params["value"] != bad {
			t.Errorf("%q: %v", bad, err)
		}
	}
	many := make([]string, MaxDNSCheckResolvers+1)
	for i := range many {
		many[i] = fmt.Sprintf("192.0.2.%d", i+1)
	}
	if _, err := ParseDNSCheckResolvers(many[:MaxDNSCheckResolvers]); err != nil {
		t.Errorf("%d resolvers: %v", MaxDNSCheckResolvers, err)
	}
	_, err = ParseDNSCheckResolvers(many)
	if m, ok := wording.As(err); !ok || m.Key != "validation.dnsCheck.resolversTooMany" || m.Params["max"] != MaxDNSCheckResolvers {
		t.Errorf("%d resolvers: %v", len(many), err)
	}
	// The default list is a fresh slice on every call.
	d := DefaultDNSCheckResolvers()
	d[0] = "192.0.2.1"
	if DefaultDNSCheckResolvers()[0] != "1.1.1.1" {
		t.Error("the default resolver list was changed through a returned slice")
	}
}

// TestListSettingSaveBounds: each sending source list saves exactly 500
// entries and refuses 501, the resolvers save exactly 10 and refuse 11; a
// refused list leaves the saved one unchanged.
func TestListSettingSaveBounds(t *testing.T) {
	db := newTestDB(t)
	numbered := func(n int, format func(i int) string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = format(i)
		}
		return out
	}
	for _, c := range []struct {
		key     string
		save    func(*sql.DB, []string) error
		resolve func(*sql.DB) []string
		entry   func(i int) string
	}{
		{SettingSendingSourceIPv4, SaveSendingSourceIPv4, ResolveSendingSourceIPv4, func(i int) string { return fmt.Sprintf("10.0.%d.%d", i/250, i%250+1) }},
		{SettingSendingSourceIPv6, SaveSendingSourceIPv6, ResolveSendingSourceIPv6, func(i int) string { return fmt.Sprintf("2001:db8::%x", i+1) }},
		{SettingSendingSourceHosts, SaveSendingSourceHosts, ResolveSendingSourceHosts, func(i int) string { return fmt.Sprintf("mx%d.example.jp", i) }},
	} {
		if err := c.save(db, numbered(500, c.entry)); err != nil {
			t.Errorf("%s: 500 entries: %v", c.key, err)
		}
		if got := c.resolve(db); len(got) != 500 {
			t.Errorf("%s: %d entries saved, want 500", c.key, len(got))
		}
		if err := c.save(db, numbered(501, c.entry)); wordingKey(err) != "validation.sendingSource.tooMany" {
			t.Errorf("%s: 501 entries: %v", c.key, err)
		}
		if got := c.resolve(db); len(got) != 500 {
			t.Errorf("%s: %d entries after a refused save, want 500", c.key, len(got))
		}
	}
	resolvers := numbered(11, func(i int) string { return fmt.Sprintf("192.0.2.%d", i+1) })
	ten := resolvers[:10]
	if err := SaveDNSCheck(db, nil, &ten, nil); err != nil {
		t.Errorf("10 resolvers: %v", err)
	}
	if got := ResolveDNSCheckResolvers(db); len(got) != 10 {
		t.Errorf("%d resolvers saved, want 10", len(got))
	}
	if err := SaveDNSCheck(db, nil, &resolvers, nil); wordingKey(err) != "validation.dnsCheck.resolversTooMany" {
		t.Errorf("11 resolvers: %v", err)
	}
	if got := ResolveDNSCheckResolvers(db); len(got) != 10 {
		t.Errorf("%d resolvers after a refused save, want 10", len(got))
	}
}

// TestListSettingSavedValueIsLenient: a saved value (edited in the database
// by hand) keeps the valid entries around an invalid one and, after
// de-duplication, only the first 500 entries of a sending source list and the
// first 10 resolvers, so the verification never fans out to more.
func TestListSettingSavedValueIsLenient(t *testing.T) {
	db := newTestDB(t)
	if err := models.SetSetting(db, SettingSendingSourceIPv4, "203.0.113.5\nbogus\n198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	if got := ResolveSendingSourceIPv4(db); !reflect.DeepEqual(got, []string{"203.0.113.5", "198.51.100.7"}) {
		t.Errorf("valid, invalid, valid: %v", got)
	}
	// A duplicate first, then 600 distinct hosts: the first 500 distinct
	// ones are kept.
	hosts := []string{"mx0.example.jp", "MX0.example.jp."}
	for i := range 600 {
		hosts = append(hosts, fmt.Sprintf("mx%d.example.jp", i))
	}
	if err := models.SetSetting(db, SettingSendingSourceHosts, strings.Join(hosts, "\n")); err != nil {
		t.Fatal(err)
	}
	if got := ResolveSendingSourceHosts(db); len(got) != 500 || got[0] != "mx0.example.jp" || got[1] != "mx1.example.jp" || got[499] != "mx499.example.jp" {
		t.Errorf("hosts over the maximum: %d entries, first %q, last %q", len(got), got[0], got[len(got)-1])
	}
	resolvers := []string{"192.0.2.1", "192.0.2.1"}
	for i := range 40 {
		resolvers = append(resolvers, fmt.Sprintf("198.51.100.%d", i+1))
	}
	if err := models.SetSetting(db, SettingDNSCheckResolvers, strings.Join(resolvers, "\n")); err != nil {
		t.Fatal(err)
	}
	got := ResolveDNSCheckResolvers(db)
	want := append([]string{"192.0.2.1"}, resolvers[2:11]...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolvers over the maximum: %v, want %v", got, want)
	}
	if cfg := ResolveDNSCheck(db); len(cfg.Resolvers) != 10 {
		t.Errorf("the verification gets %d resolvers", len(cfg.Resolvers))
	}
}

func TestValidateDNSCheck(t *testing.T) {
	some := []string{"1.1.1.1"}
	for _, tc := range []struct {
		enabled, systemOnly bool
		resolvers           []string
		ok                  bool
	}{
		{true, false, some, true},
		{true, false, nil, false},
		{true, false, []string{}, false},
		{true, true, nil, true},
		{false, false, nil, true},
		{false, true, nil, true},
		{false, false, some, true},
	} {
		err := ValidateDNSCheck(tc.enabled, tc.systemOnly, tc.resolvers)
		if (err == nil) != tc.ok || (err != nil && wordingKey(err) != "validation.dnsCheck.noResolver") {
			t.Errorf("enabled %v system only %v resolvers %v: %v", tc.enabled, tc.systemOnly, tc.resolvers, err)
		}
	}
}

func TestDNSCheckPersistence(t *testing.T) {
	db := newTestDB(t)
	cfg := ResolveDNSCheck(db)
	if !cfg.Enabled || cfg.SystemOnly || !reflect.DeepEqual(cfg.Resolvers, DefaultDNSCheckResolvers()) {
		t.Errorf("defaults: %+v", cfg)
	}
	boolp := func(b bool) *bool { return &b }
	listp := func(l ...string) *[]string {
		if l == nil {
			l = []string{}
		}
		return &l
	}
	stored := func(key string) (string, bool) {
		v, found, err := models.GetSettingStrict(db, key)
		if err != nil {
			t.Fatal(err)
		}
		return v, found
	}
	// An empty list is refused while the verification is on and not limited
	// to the OS resolver; nothing is saved.
	if err := SaveDNSCheck(db, nil, listp(), nil); wordingKey(err) != "validation.dnsCheck.noResolver" {
		t.Errorf("empty resolvers with the verification on: %v", err)
	}
	if _, found := stored(SettingDNSCheckResolvers); found {
		t.Error("a refused change was stored")
	}
	// Together with the switch off it is accepted and stored as "" (an
	// explicitly empty list, unlike no row).
	if err := SaveDNSCheck(db, boolp(false), listp(), nil); err != nil {
		t.Fatal(err)
	}
	if v, found := stored(SettingDNSCheckResolvers); !found || v != "" {
		t.Errorf("explicit empty list stored as %q (%v)", v, found)
	}
	if v, _ := stored(SettingDNSCheckEnabled); v != "0" {
		t.Errorf("dns_check_enabled stored as %q", v)
	}
	if cfg := ResolveDNSCheck(db); cfg.Enabled || cfg.Resolvers == nil || len(cfg.Resolvers) != 0 {
		t.Errorf("after saving an empty list: %#v", cfg)
	}
	// Turning the verification on again needs a resolver or the OS resolver;
	// the saved values of the absent settings count.
	if _, err := CheckDNSCheckSettings(db, boolp(true), nil, nil); wordingKey(err) != "validation.dnsCheck.noResolver" {
		t.Errorf("enabling without a resolver: %v", err)
	}
	if err := SaveDNSCheck(db, boolp(true), nil, boolp(true)); err != nil {
		t.Fatal(err)
	}
	if v, found := stored(SettingDNSCheckEnabled); found {
		t.Errorf("default dns_check_enabled still stored (%q)", v)
	}
	if v, _ := stored(SettingDNSCheckSystemResolverOnly); v != "1" {
		t.Errorf("dns_check_system_resolver_only stored as %q", v)
	}
	if err := SaveDNSCheck(db, nil, nil, boolp(false)); wordingKey(err) != "validation.dnsCheck.noResolver" {
		t.Errorf("leaving the OS resolver with no resolver: %v", err)
	}
	// The default list (same order) removes the row; another order or
	// another list is stored one address per line.
	if err := SaveDNSCheck(db, nil, listp("8.8.8.8", "1.1.1.1", "9.9.9.9"), nil); err != nil {
		t.Fatal(err)
	}
	if v, _ := stored(SettingDNSCheckResolvers); v != "8.8.8.8\n1.1.1.1\n9.9.9.9" {
		t.Errorf("reordered list stored as %q", v)
	}
	if err := SaveDNSCheck(db, nil, listp(" 1.1.1.1", "8.8.8.8", "9.9.9.9", "1.1.1.1"), boolp(false)); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{SettingDNSCheckResolvers, SettingDNSCheckSystemResolverOnly, SettingDNSCheckEnabled} {
		if v, found := stored(key); found {
			t.Errorf("default %s still stored (%q)", key, v)
		}
	}
	// A bad address refuses the whole change.
	if err := SaveDNSCheck(db, boolp(false), listp("1.1.1.1", "127.0.0.1"), nil); wordingKey(err) != "validation.dnsCheck.resolverInvalid" {
		t.Errorf("loopback resolver: %v", err)
	}
	if !ResolveDNSCheckEnabled(db) {
		t.Error("a refused change turned the verification off")
	}
	// A saved value of another shape falls back to the default, and a saved
	// entry that is not valid is skipped.
	if err := models.SetSetting(db, SettingDNSCheckEnabled, "maybe"); err != nil {
		t.Fatal(err)
	}
	if err := models.SetSetting(db, SettingDNSCheckResolvers, "9.9.9.9\nbogus"); err != nil {
		t.Fatal(err)
	}
	if cfg := ResolveDNSCheck(db); !cfg.Enabled || !reflect.DeepEqual(cfg.Resolvers, []string{"9.9.9.9"}) {
		t.Errorf("lenient resolve: %+v", cfg)
	}
}

// failSettingWrites makes every write of the setting key fail (insert,
// update or delete, through SQLite triggers) until the returned function
// removes the triggers: a failure injected halfway through a save.
func failSettingWrites(t *testing.T, db *sql.DB, key string) func() {
	t.Helper()
	names := map[string]string{"INSERT": "NEW", "UPDATE": "NEW", "DELETE": "OLD"}
	for event, row := range names {
		stmt := fmt.Sprintf(`CREATE TRIGGER fail_%s BEFORE %s ON settings WHEN %s.key = '%s'
			BEGIN SELECT RAISE(ABORT, 'injected failure'); END`, strings.ToLower(event), event, row, key)
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return func() {
		for event := range names {
			if _, err := db.Exec(`DROP TRIGGER fail_` + strings.ToLower(event)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestSettingsSavedInOneTransaction injects a failure into the last write
// of a save of several settings: nothing of the save is left behind, by
// SaveDNSCheck (inserts and a delete), by a SettingsUpdate of several
// settings and by SaveNotificationSettings, and the same save succeeds once
// the failure is gone.
func TestSettingsSavedInOneTransaction(t *testing.T) {
	db := newTestDB(t)
	boolp := func(b bool) *bool { return &b }
	listp := func(l ...string) *[]string { return &l }
	stored := func(key string) (string, bool) {
		t.Helper()
		v, found, err := models.GetSettingStrict(db, key)
		if err != nil {
			t.Fatal(err)
		}
		return v, found
	}
	untouched := func(step string, keys ...string) {
		t.Helper()
		for _, key := range keys {
			if v, found := stored(key); found {
				t.Errorf("%s: %s was saved (%q) although the save failed", step, key, v)
			}
		}
	}

	// SaveDNSCheck: the switch and the resolvers are written before the
	// OS-resolver switch, which fails.
	restore := failSettingWrites(t, db, SettingDNSCheckSystemResolverOnly)
	if err := SaveDNSCheck(db, boolp(false), listp("8.8.8.8"), boolp(true)); err == nil {
		t.Fatal("SaveDNSCheck succeeded despite the failure")
	}
	untouched("SaveDNSCheck", SettingDNSCheckEnabled, SettingDNSCheckResolvers, SettingDNSCheckSystemResolverOnly)
	restore()
	if err := SaveDNSCheck(db, boolp(true), nil, boolp(true)); err != nil {
		t.Fatal(err)
	}
	// A delete that fails (the OS-resolver switch back to its default)
	// takes the writes before it back too: the combination saved before
	// stays, never "off with a resolver list" alone.
	restore = failSettingWrites(t, db, SettingDNSCheckSystemResolverOnly)
	if err := SaveDNSCheck(db, boolp(false), listp("8.8.8.8"), boolp(false)); err == nil {
		t.Fatal("SaveDNSCheck succeeded despite the failure of the delete")
	}
	untouched("SaveDNSCheck with a delete", SettingDNSCheckEnabled, SettingDNSCheckResolvers)
	if v, _ := stored(SettingDNSCheckSystemResolverOnly); v != "1" {
		t.Errorf("dns_check_system_resolver_only = %q, want the saved 1", v)
	}
	restore()

	// A SettingsUpdate of several settings, the worker count written last.
	fill := func(u *SettingsUpdate) error {
		u.CheckTimes([]string{"07:00"})
		if err := u.SendingSourceHosts([]string{"mail.example.jp"}); err != nil {
			return err
		}
		if err := u.DNSCheck(db, boolp(false), nil, nil); err != nil {
			return err
		}
		u.Workers(5)
		return nil
	}
	restore = failSettingWrites(t, db, SettingWorkers)
	if err := UpdateSettings(db, fill); err == nil {
		t.Fatal("UpdateSettings succeeded despite the failure")
	}
	untouched("SettingsUpdate", SettingCheckTimes, SettingSendingSourceHosts, SettingDNSCheckEnabled, SettingWorkers)
	restore()
	if err := UpdateSettings(db, fill); err != nil {
		t.Fatal(err)
	}
	if ResolveWorkers(db) != 5 || ResolveDNSCheckEnabled(db) || !reflect.DeepEqual(ResolveSendingSourceHosts(db), []string{"mail.example.jp"}) {
		t.Errorf("after the save: workers %d, dns check %v, hosts %v", ResolveWorkers(db), ResolveDNSCheckEnabled(db), ResolveSendingSourceHosts(db))
	}

	// The notification settings: the sender address is written last.
	restore = failSettingWrites(t, db, SettingSMTPFrom)
	host, from := "smtp.example.jp", "alerts@example.jp"
	if err := SaveNotificationSettings(db, nil, &NotificationInput{SMTPHost: &host, SMTPFrom: &from}); err == nil {
		t.Fatal("SaveNotificationSettings succeeded despite the failure")
	}
	untouched("SaveNotificationSettings", SettingSMTPHost, SettingSMTPFrom)
	restore()
}

// TestSendingSourceMailboxesWithoutHost checks the derived list of the
// addresses for which no host name is configured: while sending_source_hosts
// is empty, every mailbox (enabled or not, in the order of the list) whose
// SMTP host is empty or not a valid host name; none once sending_source_hosts
// holds a host name, which counts for every mailbox.
func TestSendingSourceMailboxesWithoutHost(t *testing.T) {
	db := newTestDB(t)
	for _, mb := range []*models.Mailbox{
		{Address: "with-host@b.example.jp", Enabled: true, SmtpHost: "smtp.b.example.jp"},
		{Address: "user1@a.example.jp", Enabled: true},
		{Address: "user2@c.example.jp", Enabled: false},
		{Address: "broken@b.example.jp", Enabled: true, SmtpHost: "203.0.113.5"}, // edited by hand: no host name
	} {
		if err := models.InsertMailbox(db, mb); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := ResolveSendingSources(db)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"user1@a.example.jp", "broken@b.example.jp", "user2@c.example.jp"}; !reflect.DeepEqual(sources.MailboxesWithoutHost, want) {
		t.Errorf("without sending_source_hosts: %v, want %v", sources.MailboxesWithoutHost, want)
	}
	// Addresses alone switch nothing on: the list stays.
	if err := SaveSendingSourceIPv4(db, []string{"203.0.113.0/24"}); err != nil {
		t.Fatal(err)
	}
	if sources, err := ResolveSendingSources(db); err != nil || len(sources.MailboxesWithoutHost) != 3 {
		t.Errorf("with addresses only: %v (err %v)", sources.MailboxesWithoutHost, err)
	}
	if err := SaveSendingSourceHosts(db, []string{"relay.example.jp"}); err != nil {
		t.Fatal(err)
	}
	if sources, err := ResolveSendingSources(db); err != nil || sources.MailboxesWithoutHost == nil || len(sources.MailboxesWithoutHost) != 0 {
		t.Errorf("with sending_source_hosts: %#v (err %v), want []", sources.MailboxesWithoutHost, err)
	}
}

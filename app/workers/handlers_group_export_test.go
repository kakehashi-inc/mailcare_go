package workers

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
)

func TestGroupReportPDF(t *testing.T) {
	s := newSeededCore(t)
	rec := do(t, s.h, http.MethodGet, s.path("/groups?scope=all&q=nowhere.example.org"), nil, s.user)
	var list groupsResponse
	decode(t, rec.Body.Bytes(), &list)
	if len(list.Groups) != 1 {
		t.Fatalf("seeded group: %+v", list.Groups)
	}
	gk := list.Groups[0].GroupKey
	rec = do(t, s.h, http.MethodGet, s.path("/groups/"+gk+"/report"), nil, s.user)
	if rec.Code != http.StatusOK {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("content type %q", ct)
	}
	disposition := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(disposition, `attachment; filename="mailcare-group-`+gk+"-") || !strings.HasSuffix(disposition, `.pdf"`) {
		t.Errorf("disposition %q", disposition)
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("%PDF-")) {
		t.Errorf("body is not a PDF")
	}
	if rec := do(t, s.h, http.MethodGet, s.path("/groups/0000000000000000/report"), nil, s.user); rec.Code != http.StatusNotFound {
		t.Errorf("unknown group: %d", rec.Code)
	}
}

func TestGroupExport(t *testing.T) {
	s := newSeededCore(t)
	rec := do(t, s.h, http.MethodGet, s.path("/groups?scope=all&q=nowhere.example.org"), nil, s.user)
	var list groupsResponse
	decode(t, rec.Body.Bytes(), &list)
	if len(list.Groups) != 1 {
		t.Fatalf("seeded group: %+v", list.Groups)
	}
	gk := list.Groups[0].GroupKey

	// Every signed-in user may export.
	rec = do(t, s.h, http.MethodGet, s.path("/groups/"+gk+"/export"), nil, s.user)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("content type %q", ct)
	}
	disposition := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(disposition, `attachment; filename="mailcare-group-`+gk+"-") || !strings.HasSuffix(disposition, `.zip"`) {
		t.Errorf("disposition %q", disposition)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	entries := map[string]string{}
	for _, f := range zr.File {
		name := f.Name
		// The entries sit at the top level, with no folder named like the archive.
		if dir, _, ok := strings.Cut(name, "/"); ok && dir+"/" != groupExportMailsDir {
			t.Fatalf("entry %s is not at the top level or in mails/", name)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		entries[name] = string(data)
	}

	var export GroupExportDTO
	decode(t, []byte(entries[groupExportJSONName]), &export)
	if export.Group.GroupKey != gk || export.Mailbox.Address != s.mb.Address || export.ExportedAt == "" {
		t.Errorf("export head: %+v %+v", export.Group, export.Mailbox)
	}
	if export.Stats == nil || len(export.Stats.Recipients) != 1 || export.Stats.Recipients[0] != "jiro@nowhere.example.org" {
		t.Errorf("stats %+v", export.Stats)
	}
	if len(export.Messages) != 1 {
		t.Fatalf("messages %+v", export.Messages)
	}
	m := export.Messages[0]
	if m.Bounce == nil || m.Bounce.GroupKey != gk || m.Bounce.ID != m.Message.ID || m.Bounce.PatternKey == "" ||
		m.Bounce.Recipient != "jiro@nowhere.example.org" || len(m.DMARCRecords) != 0 {
		t.Errorf("index rows %+v %+v", m.Message, m.Bounce)
	}
	// Every listed file is in the archive, and the archive holds nothing else.
	key := m.Message.MessageKey
	want := []string{groupExportMailsDir + key + ".eml", groupExportMailsDir + key + "-headers.txt"}
	for n := 1; n <= m.Message.TextCount; n++ {
		want = append(want, groupExportMailsDir+key+"-"+itoa(int64(n))+".txt")
	}
	for n := 1; n <= m.Message.HTMLCount; n++ {
		want = append(want, groupExportMailsDir+key+"-"+itoa(int64(n))+".html")
	}
	got := append([]string(nil), m.Files...)
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("files %v, want %v", got, want)
	}
	if len(entries) != len(want)+2 {
		t.Errorf("archive has %d entries, want %d", len(entries), len(want)+2)
	}
	if !strings.HasPrefix(entries[groupExportReadmeName], "# MailCare group export") {
		t.Errorf("README.md: %.60q", entries[groupExportReadmeName])
	}
	for _, f := range m.Files {
		if _, ok := entries[f]; !ok {
			t.Errorf("listed file %s missing from the archive", f)
		}
	}
	if !strings.Contains(entries[groupExportMailsDir+key+"-headers.txt"], "Subject: ") {
		t.Errorf("header list: %q", entries[groupExportMailsDir+key+"-headers.txt"])
	}

	rec = do(t, s.h, http.MethodGet, s.path("/groups/0000000000000000/export"), nil, s.user)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown group: %d", rec.Code)
	}
	rec = do(t, s.h, http.MethodGet, s.path("/groups/not-a-key/export"), nil, s.user)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed group key: %d", rec.Code)
	}
}

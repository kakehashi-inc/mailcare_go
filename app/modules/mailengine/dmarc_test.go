package mailengine

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
)

// dmarcXML builds an aggregate report for example.jp with the given records
// (each a <record> element body).
func dmarcXML(records ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<feedback>
  <report_metadata><org_name>receiver.example.net</org_name><report_id>r-1</report_id>
    <date_range><begin>1780790400</begin><end>1780876799</end></date_range></report_metadata>
  <policy_published><domain>example.jp</domain><p>reject</p></policy_published>
`)
	for _, r := range records {
		b.WriteString("  <record>" + r + "</record>\n")
	}
	b.WriteString("</feedback>\n")
	return b.String()
}

// dmarcRecordXML is one <record>: source IP, count, evaluated dkim / spf,
// header_from, and the auth_results entries as raw XML.
func dmarcRecordXML(ip, count, dkim, spf, headerFrom, auth string) string {
	return `<row><source_ip>` + ip + `</source_ip><count>` + count + `</count><policy_evaluated><disposition>reject</disposition><dkim>` +
		dkim + `</dkim><spf>` + spf + `</spf></policy_evaluated></row><identifiers><header_from>` + headerFrom +
		`</header_from></identifiers><auth_results>` + auth + `</auth_results>`
}

// dmarcMail wraps a report attachment into a mail.
func dmarcMail(contentType, name string, attachment []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(attachment)
	return []byte("From: noreply-dmarc@receiver.example.net\r\nTo: dmarc@example.jp\r\n" +
		"Subject: Report domain: example.jp Submitter: receiver.example.net Report-ID: r-1\r\n" +
		"Message-ID: <r-1@receiver.example.net>\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain\r\n\r\nThis is an aggregate report.\r\n" +
		"--b1\r\nContent-Type: " + contentType + "; name=\"" + name + "\"\r\n" +
		"Content-Disposition: attachment; filename=\"" + name + "\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		enc + "\r\n--b1--\r\n")
}

func zipped(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gzipped(t *testing.T, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The three failing shapes seen in real reports plus a passing record.
var (
	recSPFMissing = dmarcRecordXML("192.0.2.10", "3", "fail", "fail", "example.jp",
		`<spf><domain>mail.example.jp</domain><result>none</result><scope>helo</scope></spf>`)
	recDKIMFailed = dmarcRecordXML("198.51.100.7", "2", "fail", "fail", "example.jp",
		`<dkim><domain>example.jp</domain><result>fail</result><selector>s1</selector></dkim>`+
			`<spf><domain>example.jp</domain><result>softfail</result></spf>`)
	recUnaligned = dmarcRecordXML("203.0.113.9", "1", "fail", "fail", "example.jp",
		`<dkim><domain>other.example.org</domain><result>fail</result></dkim>`+
			`<spf><domain>spoof.example.biz</domain><result>pass</result></spf>`)
	recPassed = dmarcRecordXML("192.0.2.20", "40", "pass", "pass", "example.jp",
		`<dkim><domain>example.jp</domain><result>pass</result></dkim><spf><domain>example.jp</domain><result>pass</result></spf>`)
)

func TestParseDMARCAttachmentFormats(t *testing.T) {
	doc := dmarcXML(recPassed, recSPFMissing)
	for _, c := range []struct {
		name, contentType, file string
		data                    []byte
	}{
		{"zip", "application/zip", "receiver.example.net!example.jp!1780790400!1780876799.zip", zipped(t, "report.xml", doc)},
		{"gzip", "application/gzip", "receiver.example.net!example.jp!1780790400!1780876799.xml.gz", gzipped(t, doc)},
		{"xml declared as octet-stream", "application/octet-stream", "report.xml", []byte(doc)},
	} {
		t.Run(c.name, func(t *testing.T) {
			pm := ParseMessage(dmarcMail(c.contentType, c.file, c.data))
			rep := pm.DMARC
			if rep == nil {
				t.Fatal("no DMARC report parsed")
			}
			if rep.OrgName != "receiver.example.net" || rep.Domain != "example.jp" || rep.Policy != "reject" ||
				!rep.Begin.Equal(time.Unix(1780790400, 0)) || len(rep.Records) != 2 {
				t.Errorf("report = %+v", rep)
			}
			if rep.Records[0].Failed() || !rep.Records[1].Failed() {
				t.Errorf("failed flags = %v / %v, want false / true", rep.Records[0].Failed(), rep.Records[1].Failed())
			}
			cls := Classify(pm, "dmarc@example.jp")
			if !cls.IsBounce || cls.Kind != bounceKindReport || cls.Rule != ruleDMARCReport {
				t.Errorf("classification = %+v", cls)
			}
		})
	}
	// Another XML document, a broken archive and an image are no report.
	for name, data := range map[string][]byte{
		"other xml": []byte(`<?xml version="1.0"?><invoice><total>1</total></invoice>`),
		"broken":    []byte("PK\x03\x04broken"),
		"image":     {0x89, 'P', 'N', 'G'},
	} {
		if pm := ParseMessage(dmarcMail("application/octet-stream", "x.bin", data)); pm.DMARC != nil {
			t.Errorf("%s: parsed as a DMARC report", name)
		}
	}
}

// TestParseDMARCZipLimits: a zip attachment is tried for at most
// maxDMARCZipMembers members, and members that do not start like XML are
// skipped without being read whole.
func TestParseDMARCZipLimits(t *testing.T) {
	archive := func(before int) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for i := 0; i < before; i++ {
			w, _ := zw.Create(fmt.Sprintf("junk%d.bin", i))
			_, _ = w.Write(bytes.Repeat([]byte{0}, 1<<20))
		}
		w, _ := zw.Create("report.xml")
		_, _ = w.Write([]byte(dmarcXML(recSPFMissing)))
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	if parseDMARCAttachment(archive(maxDMARCZipMembers-1)) == nil {
		t.Error("the report after the skipped members was not found")
	}
	if parseDMARCAttachment(archive(maxDMARCZipMembers)) != nil {
		t.Error("a report beyond the member limit was read")
	}
	if parseDMARCAttachment(append([]byte(dmarcXML(recSPFMissing)), bytes.Repeat([]byte(" "), maxDMARCReportSize)...)) != nil {
		t.Error("an XML document above the size limit was read")
	}
}

func TestCategorizeDMARCRecord(t *testing.T) {
	pm := ParseMessage(dmarcMail("application/zip", "r.zip", zipped(t, "r.xml", dmarcXML(recSPFMissing, recDKIMFailed, recUnaligned))))
	if pm.DMARC == nil || len(pm.DMARC.Records) != 3 {
		t.Fatalf("report = %+v", pm.DMARC)
	}
	want := []struct{ category, rule string }{
		{categoryDMARCSPFMissing, dmarcRuleSPFMissing},
		{categoryDMARCDKIMFailed, dmarcRuleDKIMFailed},
		{categoryDMARCNotAuthenticated, dmarcRuleUnaligned},
	}
	for i, w := range want {
		c := CategorizeDMARCRecord(pm.DMARC, &pm.DMARC.Records[i])
		if c.Category != w.category || c.Reason != w.rule || !c.Actionable || c.UnitValue != "example.jp" || c.Authority != "" {
			t.Errorf("record %d = %+v, want %s by %s (actionable, unit example.jp)", i, c, w.category, w.rule)
		}
	}
	// An aligned SPF domain that does not authorize the IP.
	notAuth := DMARCRecord{DKIM: "fail", SPF: "fail", HeaderFrom: "example.jp",
		SPFResults: []DMARCAuthResult{{Domain: "example.jp", Result: "softfail"}}}
	if c := CategorizeDMARCRecord(pm.DMARC, &notAuth); c.Category != categoryDMARCNotAuthenticated || c.Reason != dmarcRuleNotAuthorized {
		t.Errorf("not authorized = %+v", c)
	}
	// Strict alignment: the subdomain passed but DMARC failed.
	strict := DMARCRecord{DKIM: "fail", SPF: "fail", HeaderFrom: "example.jp",
		SPFResults: []DMARCAuthResult{{Domain: "mail.example.jp", Result: "pass"}}}
	if c := CategorizeDMARCRecord(pm.DMARC, &strict); c.Category != categoryDMARCNotAuthenticated || c.Reason != dmarcRuleStrictAlign {
		t.Errorf("strict alignment = %+v", c)
	}
	// The template leaves the sending IP and foreign domains out.
	if got := DMARCTemplate(pm.DMARC, &pm.DMARC.Records[2]); got != "dmarc fail: dkim unaligned; spf unaligned; disposition reject" {
		t.Errorf("template = %q", got)
	}
	if got := DMARCTemplate(pm.DMARC, &pm.DMARC.Records[0]); got != "dmarc fail: dkim none; spf aligned none; disposition reject" {
		t.Errorf("template = %q", got)
	}
}

// TestGroupDMARCReport: the failing records of a report are filed into
// actionable groups (one per category and domain), the passing ones are not
// kept, the groups count the report mail once and list the sending IPs, and
// a reclassification or the removal of the mail keeps the groups in step.
func TestGroupDMARCReport(t *testing.T) {
	root := t.TempDir()
	address := "dmarc@example.jp"
	dir := MailboxDir(root, address)
	db := mustOpenIndex(t, root, address)
	doc := dmarcXML(recPassed, recSPFMissing, recDKIMFailed, recUnaligned,
		dmarcRecordXML("203.0.113.10", "1", "fail", "fail", "example.jp",
			`<dkim><domain>other2.example.org</domain><result>fail</result></dkim>`+
				`<spf><domain>spoof2.example.biz</domain><result>pass</result></spf>`))
	raw := dmarcMail("application/zip", "r.zip", zipped(t, "r.xml", doc))
	msg, _, err := storeMessage(db, dir, raw, Source{Folder: "INBOX", UIDValidity: 1, UID: 1, ReceivedAt: time.Now().UTC()}, storeOptions{writeEML: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := GroupMailbox(context.Background(), root, address, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Processed != 1 || res.Bounces != 1 || res.Groups != 3 || len(res.GroupsTouched) != 3 {
		t.Fatalf("group result = %+v, want 1 processed, 3 actionable groups flagged", res)
	}
	var records int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dmarc_records WHERE message_id = ?`, msg.ID).Scan(&records); err != nil || records != 4 {
		t.Fatalf("records = %d (err %v), want the 4 failing ones", records, err)
	}
	notAuth := GroupKey(categoryDMARCNotAuthenticated, "example.jp", "")
	g, err := models.GetGroup(db, notAuth)
	if err != nil {
		t.Fatal(err)
	}
	if !g.Actionable || g.MessageCount != 1 || g.RemoteIPCount != 2 || g.Responsible != responsibleUnknown {
		t.Errorf("not-authenticated group = %+v, want actionable, 1 message, 2 sending IPs", g)
	}
	keys, err := models.ListGroupPatternKeys(db, notAuth)
	if err != nil || len(keys) != 1 {
		t.Errorf("patterns = %v (err %v), want one pattern for both unaligned sources (foreign domains and IPs differ)", keys, err)
	}
	members, err := models.ListGroupBounces(db, notAuth)
	if err != nil || len(members) != 2 || members[0].DMARC == nil || members[0].DiagnosticSource != DiagnosticSourceDMARC {
		t.Fatalf("members = %+v (err %v)", members, err)
	}
	stats, err := models.GroupStats(db, notAuth)
	if err != nil || len(stats.RemoteIPs) != 2 {
		t.Errorf("stats = %+v (err %v), want the 2 sending IPs", stats, err)
	}
	list, total, err := models.ListMessages(db, models.MessageFilter{GroupKey: notAuth})
	if err != nil || total != 1 || len(list) != 1 || list[0].BounceKind != bounceKindReport {
		t.Errorf("group messages = %+v (total %d, err %v)", list, total, err)
	}
	if n, _ := CountTargetMessages(db); n != 1 {
		t.Errorf("target messages = %d, want the report", n)
	}

	// A full reclassification rebuilds the same groups.
	if res, err = GroupMailbox(context.Background(), root, address, true, nil); err != nil || res.Groups != 3 {
		t.Fatalf("full regroup = %+v (err %v)", res, err)
	}
	// The mail retention removes the report: its records and groups go.
	if _, err := db.Exec(`UPDATE messages SET date = ? WHERE id = ?`, time.Now().AddDate(-1, 0, 0).UTC(), msg.ID); err != nil {
		t.Fatal(err)
	}
	pr, err := PruneMailbox(context.Background(), root, address, 24*time.Hour, nil)
	if err != nil || pr.Removed != 1 || pr.RemovedGroups != 3 {
		t.Errorf("prune = %+v (err %v), want the report and its 3 groups removed", pr, err)
	}
}

// TestCategorizeDMARCUnsignedDKIM: an aligned DKIM result "none" is no
// signature, so the record is not filed as a failed DKIM verification.
func TestCategorizeDMARCUnsignedDKIM(t *testing.T) {
	rep := &DMARCReport{Domain: "example.jp"}
	r := DMARCRecord{DKIM: "fail", SPF: "fail", HeaderFrom: "example.jp",
		DKIMResults: []DMARCAuthResult{{Domain: "example.jp", Result: "none"}},
		SPFResults:  []DMARCAuthResult{{Domain: "example.jp", Result: "softfail"}}}
	if c := CategorizeDMARCRecord(rep, &r); c.Category != categoryDMARCNotAuthenticated || c.Reason != dmarcRuleNotAuthorized {
		t.Errorf("unsigned = %+v, want dmarc_not_authenticated by dmarc_not_authorized", c)
	}
}

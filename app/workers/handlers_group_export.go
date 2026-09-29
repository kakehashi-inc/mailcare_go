package workers

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules"
	"mailcare/app/modules/mailengine"
	"mailcare/app/modules/pdfreport"
)

// groupExportWriteTimeout replaces the server's WriteTimeout for a group
// export: the archive carries every member mail, which can take longer to
// send than an ordinary response.
const groupExportWriteTimeout = 30 * time.Minute

// Names inside the archive of a group export (at its top level: the archive
// has no folder of its own).
const (
	groupExportJSONName   = "report.json"
	groupExportReadmeName = "README.md"
	groupExportMailsDir   = "mails/"
)

// groupExportReadme is the README.md of every group export (in
// EmbeddedFS): the files of the extracted folder and the format of
// report.json, written in English for the agents and tools that read them.
const groupExportReadme = "templates/export/group_README.md"

// groupOutputName is the file name base of the PDF report and of the
// export of a group (the export time in UTC).
func groupOutputName(groupKey string, now time.Time) string {
	return "mailcare-group-" + groupKey + "-" + now.Format("20060102-150405")
}

// groupReports returns the newest completed report and the newest report
// of any status of a group (nil when there is none).
func groupReports(idx *sql.DB, groupKey string) (completed, latest *models.AgentReport, err error) {
	completed, err = models.LatestCompletedAgentReport(idx, groupKey)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	latest, err = models.LatestAgentReport(idx, groupKey)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	return completed, latest, nil
}

// handleGroupReport sends the PDF report of a group (pdfreport.WriteGroupReport:
// the overview and the statistics of the group detail) in the language and
// the time zone of the signed-in user, as a download.
func (c *core) handleGroupReport(w http.ResponseWriter, r *http.Request) {
	mb, idx, g, ok := c.groupFromPath(w, r)
	if !ok {
		return
	}
	defer idx.Close()
	stats, err := models.GroupStats(idx, g.GroupKey)
	if err != nil {
		writeInternalError(w, "failed to load group statistics", err)
		return
	}
	completed, latest, err := groupReports(idx, g.GroupKey)
	if err != nil {
		writeInternalError(w, "failed to load reports", err)
		return
	}
	dto := toGroupDTO(g, completed, latest)
	locale := modules.MailLocaleFor(userFrom(r))
	lang, err := modules.LoadLanguageFile(locale.Language)
	if err != nil {
		writeInternalError(w, "failed to load the language file", err)
		return
	}
	mailbox := mb.Address
	if mb.DisplayName != "" {
		mailbox = mb.DisplayName + " (" + mb.Address + ")"
	}
	now := time.Now().UTC()
	var buf bytes.Buffer
	err = pdfreport.WriteGroupReport(&buf, &pdfreport.GroupReport{
		Mailbox: mailbox, Group: g, Stats: stats,
		Analysis: pdfreport.Analysis{
			Severity: dto.ReportSeverity, NeedsReview: dto.ReportConfidence != nil && *dto.ReportConfidence == "low",
			Status: dto.ReportStatus, Unanalyzable: dto.ReportUnanalyzable,
		},
		GeneratedAt: now, Location: locale.Location, Language: lang.Language, Creator: "MailCare " + modules.AppVersion,
	}, lang.Lookup)
	if err != nil {
		writeInternalError(w, "failed to render the group report", err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+groupOutputName(g.GroupKey, now)+`.pdf"`)
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// exportMessageFiles are the files of one member message that exist on
// disk: the raw file (EML, "" when missing; the decoded header list is
// written from it) and the body section files.
type exportMessageFiles struct {
	key      string
	eml      string
	sections []string
}

// handleExportGroup sends a group as a ZIP archive: report.json
// (GroupExportDTO: the overview, the statistics and the index rows of every
// member message), README.md (groupExportReadme: the same in every
// archive) and mails/ with, for every member message, the raw
// <key>.eml, the decoded header list <key>-headers.txt and the body section
// files <key>-<n>.txt / <key>-<n>.html, all at the top level of the archive
// (no folder named like the archive).
func (c *core) handleExportGroup(w http.ResponseWriter, r *http.Request) {
	mb, idx, g, ok := c.groupFromPath(w, r)
	if !ok {
		return
	}
	defer idx.Close()
	now := time.Now().UTC()
	export, plans, err := c.buildGroupExport(idx, mb, g, now)
	if err != nil {
		writeInternalError(w, "failed to build the group export", err)
		return
	}
	body, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		writeInternalError(w, "failed to encode the group export", err)
		return
	}
	if modules.EmbeddedFS == nil {
		writeInternalError(w, "failed to read the export README", errors.New("templates are not available"))
		return
	}
	readme, err := fs.ReadFile(modules.EmbeddedFS, groupExportReadme)
	if err != nil {
		writeInternalError(w, "failed to read the export README", err)
		return
	}

	name := groupOutputName(g.GroupKey, now)
	// A controller that cannot change the deadline keeps the server's.
	_ = http.NewResponseController(w).SetWriteDeadline(now.Add(groupExportWriteTimeout))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.zip"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)

	// From here on the status is sent: a failure can only cut the archive
	// short, so it is logged and the response ends.
	zw := zip.NewWriter(w)
	for _, e := range []struct {
		name string
		data []byte
	}{{groupExportReadmeName, readme}, {groupExportJSONName, body}} {
		if err := writeZipEntry(zw, e.name, now, e.data); err != nil {
			log.Printf("group export %s: %v", g.GroupKey, err)
			return
		}
	}
	for _, p := range plans {
		if err := writeExportMessage(zw, groupExportMailsDir, p); err != nil {
			log.Printf("group export %s: %v", g.GroupKey, err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		log.Printf("group export %s: %v", g.GroupKey, err)
	}
}

// buildGroupExport loads what report.json holds and finds the files of
// every member message.
func (c *core) buildGroupExport(idx *sql.DB, mb *models.Mailbox, g *models.BounceGroup, now time.Time) (*GroupExportDTO, []exportMessageFiles, error) {
	stats, err := models.GroupStats(idx, g.GroupKey)
	if err != nil {
		return nil, nil, err
	}
	completed, latest, err := groupReports(idx, g.GroupKey)
	if err != nil {
		return nil, nil, err
	}
	messages, err := models.ListGroupMessages(idx, g.GroupKey)
	if err != nil {
		return nil, nil, err
	}
	bounces, err := models.ListBouncesByGroup(idx, g.GroupKey)
	if err != nil {
		return nil, nil, err
	}
	bounceOf := make(map[int64]*models.Bounce, len(bounces))
	for _, b := range bounces {
		bounceOf[b.ID] = b
	}
	records, err := models.ListDMARCRecordsByGroup(idx, g.GroupKey)
	if err != nil {
		return nil, nil, err
	}
	recordsOf := map[int64][]DMARCRecordDTO{}
	for _, rec := range records {
		recordsOf[rec.MessageID] = append(recordsOf[rec.MessageID], toDMARCRecordDTO(rec))
	}

	export := &GroupExportDTO{
		ExportedAt: timeString(now),
		Mailbox:    GroupExportMailboxDTO{ID: mb.ID, Address: mb.Address, DisplayName: mb.DisplayName},
		Group:      toGroupDTO(g, completed, latest),
		Stats:      stats,
		Messages:   make([]GroupExportMessageDTO, 0, len(messages)),
	}
	plans := make([]exportMessageFiles, 0, len(messages))
	for _, m := range messages {
		p, err := c.findMessageFiles(mb, m)
		if err != nil {
			return nil, nil, err
		}
		files := []string{}
		if p.eml != "" {
			files = append(files, groupExportMailsDir+filepath.Base(p.eml), groupExportMailsDir+headerFileName(m.MessageKey))
		}
		for _, s := range p.sections {
			files = append(files, groupExportMailsDir+filepath.Base(s))
		}
		dmarc := recordsOf[m.ID]
		if dmarc == nil {
			dmarc = []DMARCRecordDTO{}
		}
		export.Messages = append(export.Messages, GroupExportMessageDTO{
			Message: toMessageDTO(m), Bounce: toBounceRecordDTO(bounceOf[m.ID]), DMARCRecords: dmarc, Files: files,
		})
		plans = append(plans, p)
	}
	return export, plans, nil
}

// findMessageFiles lists the files of a message that exist: the raw file
// and the text_count / html_count body sections (a missing file is left
// out).
func (c *core) findMessageFiles(mb *models.Mailbox, m *models.Message) (exportMessageFiles, error) {
	p := exportMessageFiles{key: m.MessageKey}
	exists := func(path string) (bool, error) {
		if path == "" {
			return false, nil
		}
		_, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	}
	eml := mailengine.MessageFilePath(c.mailsRoot, mb.Address, m.MessageKey, "eml")
	if ok, err := exists(eml); err != nil {
		return p, err
	} else if ok {
		p.eml = eml
	}
	dir := mailengine.MailboxDir(c.mailsRoot, mb.Address)
	for _, kind := range []struct {
		ext   string
		count int
	}{{"txt", m.TextCount}, {"html", m.HTMLCount}} {
		for n := 1; n <= kind.count; n++ {
			path := mailengine.SectionFilePath(dir, m.MessageKey, kind.ext, n)
			ok, err := exists(path)
			if err != nil {
				return p, err
			}
			if ok {
				p.sections = append(p.sections, path)
			}
		}
	}
	return p, nil
}

// headerFileName is the archive name of the decoded header list of a message.
func headerFileName(key string) string {
	return key + "-headers.txt"
}

// writeExportMessage adds the files of one message under dir. A file that
// vanished since it was listed (the retention cleanup ran meanwhile) is
// left out.
func writeExportMessage(zw *zip.Writer, dir string, p exportMessageFiles) error {
	if p.eml != "" {
		raw, err := os.ReadFile(p.eml)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return err
		default:
			modified := fileModTime(p.eml)
			if err := writeZipEntry(zw, dir+filepath.Base(p.eml), modified, raw); err != nil {
				return err
			}
			if err := writeZipEntry(zw, dir+headerFileName(p.key), modified, []byte(mailengine.HeaderText(raw))); err != nil {
				return err
			}
		}
	}
	for _, s := range p.sections {
		if err := copyZipEntry(zw, dir+filepath.Base(s), s); err != nil {
			return err
		}
	}
	return nil
}

// writeZipEntry adds one compressed file with the given content.
func writeZipEntry(zw *zip.Writer, name string, modified time.Time, data []byte) error {
	f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: modified})
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return err
}

// copyZipEntry adds the file at path as one compressed entry; a missing
// file is skipped.
func copyZipEntry(zw *zip.Writer, name, path string) error {
	src, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer src.Close()
	f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: fileModTime(path)})
	if err != nil {
		return err
	}
	_, err = io.Copy(f, src)
	return err
}

// fileModTime is the modification time of a file (the current time when it
// cannot be read).
func fileModTime(path string) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return time.Now()
}

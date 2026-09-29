package pdfreport

import (
	"database/sql"
	"io"
	"strconv"
	"time"

	"mailcare/app/models"
)

// Translate returns the text of a full key of the Web UI's language files
// with its placeholders replaced; ok is false when there is no text for the
// key (modules.LanguageFile.Lookup).
type Translate func(key string, params map[string]any) (text string, ok bool)

// Analysis is what the overview shows of the analysis reports of a group
// (the report_* fields of the Web UI's GroupDTO).
type Analysis struct {
	Severity     string // of the newest completed report ("" when none)
	NeedsReview  bool   // the newest completed report has confidence "low"
	Status       string // of the newest report: running | completed | error | ""
	Unanalyzable bool   // the newest report failed for good
}

// GroupReport is the content of the PDF report of a group: the overview and
// the statistics of the group detail.
type GroupReport struct {
	Mailbox     string // the mail address (with its display name)
	Group       *models.BounceGroup
	Analysis    Analysis
	Stats       *models.GroupBounceStats
	GeneratedAt time.Time
	Location    *time.Location // dates and times of the report
	Language    string         // of the translations (ja | en), for the date format and the PDF language
	Creator     string         // the application and its version
}

// Badge tones of the categories (frontend/src/utils/category.ts); an
// unknown category is neutral.
var categoryTones = map[string]tone{
	"ip_blocked": toneDanger, "rate_limited": toneWarning, "auth_failure": toneDanger,
	"sender_blocked": toneDanger, "content_rejected": toneWarning, "message_too_large": toneWarning,
	"server_config": toneDanger, "unknown_failure": toneWarning, "dmarc_spf_missing": toneDanger,
	"dmarc_dkim_failed": toneWarning, "dmarc_not_authenticated": toneWarning, "user_unknown": toneNeutral,
	"mailbox_full": toneNeutral, "mailbox_disabled": toneNeutral, "domain_not_found": toneNeutral,
	"delivery_delay": toneInfo,
}

var stateTones = map[string]tone{"open": toneDanger, "resolved": toneSuccess, "ignored": toneNeutral}

var severityTones = map[string]tone{"high": toneDanger, "medium": toneWarning, "low": toneInfo}

// GroupHeadline is the localized one-line title of a group, built like the
// Web UI's groupHeadline (frontend/src/utils/category.ts).
func GroupHeadline(g *models.BounceGroup, tr Translate) string {
	label, ok := tr("value.category."+g.Category+".label", nil)
	if !ok {
		label = g.Category
	}
	authority := g.Authority
	if g.Category == "unknown_failure" {
		authority = ""
	}
	t := func(key string, params map[string]any) string {
		text, _ := tr(key, params)
		return text
	}
	if g.UnitValue != "" {
		if authority != "" {
			return t("field.group.titleWithAuthority", map[string]any{"label": label, "unit": g.UnitValue, "authority": authority})
		}
		return t("field.group.title", map[string]any{"label": label, "unit": g.UnitValue})
	}
	where := authority
	if where == "" {
		where = g.RecipientDomain
	}
	if where == "" {
		return label
	}
	return t("field.group.titleNoUnit", map[string]any{"label": label, "where": where})
}

// WriteGroupReport renders the report of a group as a PDF: a header (the
// headline, the mail address, when it was generated), the overview (the
// status badges, what the category asks for and the facts of the group)
// and the statistics (every recipient, IP and remote MTA), laid out like
// the group detail of the Web UI.
func WriteGroupReport(w io.Writer, rep *GroupReport, tr Translate) error {
	t := func(key string, params map[string]any) string {
		if text, ok := tr(key, params); ok {
			return text
		}
		return key
	}
	g := rep.Group
	headline := GroupHeadline(g, tr)
	title := t("page.groupReport.title", nil)
	d, err := newDocument("MailCare - " + title + " - " + headline)
	if err != nil {
		return err
	}
	d.pdf.SetTitle(headline+" - "+title, true)
	d.pdf.SetCreator(rep.Creator, true)
	d.pdf.SetLang(rep.Language)
	d.pdf.SetCreationDate(rep.GeneratedAt)

	// Header.
	d.setFont(familyText, "B", 9)
	d.setTextColor(colorAccent)
	d.pdf.SetXY(marginX, marginTop)
	d.pdf.CellFormat(d.textWidth("MailCare "), lineHeight(9), "MailCare ", "", 0, "L", false, 0, "")
	d.setFont(familyText, "", 9)
	d.setTextColor(colorMuted)
	d.pdf.CellFormat(0, lineHeight(9), title, "", 2, "L", false, 0, "")
	d.space(1)
	d.setFont(familyText, "B", 17)
	d.setTextColor(colorInk)
	d.lines(d.wrap(headline, d.width), marginX, d.width, lineHeight(17)*0.9)
	d.space(2)
	meta := [][2]string{
		{t("field.mailbox.address", nil), rep.Mailbox},
		{t("page.groupReport.generatedAt", nil), formatTime(rep.GeneratedAt, rep.Location, rep.Language) + " (" + rep.Location.String() + ")"},
	}
	d.setFont(familyText, "", 9)
	labelW := 0.0
	for _, m := range meta {
		labelW = max(labelW, d.textWidth(m[0]))
	}
	labelW += 5
	for _, m := range meta {
		y := d.pdf.GetY()
		d.setFont(familyText, "", 9)
		d.setTextColor(colorMuted)
		d.pdf.SetXY(marginX, y)
		d.pdf.CellFormat(labelW, lineHeight(9), m[0], "", 0, "L", false, 0, "")
		d.setTextColor(colorInk)
		d.lines(d.wrap(m[1], d.width-labelW), marginX+labelW, d.width-labelW, lineHeight(9))
	}
	d.space(3)
	d.setDrawColor(colorInk)
	d.pdf.SetLineWidth(0.6)
	y := d.pdf.GetY()
	d.pdf.Line(marginX, y, marginX+d.width, y)
	d.space(7)

	// Overview.
	d.heading(t("page.groupDetail.overview", nil), 12)
	d.badges(groupBadges(g, rep.Analysis, t, tr))
	d.space(4)
	if desc, ok := tr("value.category."+g.Category+".description", nil); ok && desc != "" {
		accent := colorAccent
		size := 10.0
		d.setFont(familyText, "", size)
		lines := d.wrap(desc, d.width-2*3.5-1.2)
		d.box(lines, marginX, d.width, lineHeight(size), 3.5, colorWell, &accent, func() {
			d.setFont(familyText, "", size)
			d.setTextColor(colorInk)
		})
		d.space(5)
	}
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	stamp := func(v sql.NullTime) string {
		if !v.Valid {
			return "-"
		}
		return formatTime(v.Time, rep.Location, rep.Language)
	}
	d.fields([]field{
		{label: t("field.group.unitValue", nil), value: dash(g.UnitValue), kind: fieldCode},
		{label: t("field.group.authority", nil), value: dash(g.Authority), kind: fieldCode},
		{label: t("field.common.recipientDomain", nil), value: dash(g.RecipientDomain)},
		{label: t("field.group.statusCode", nil), value: dash(g.StatusCode)},
		{label: t("field.group.messageCount", nil), value: strconv.Itoa(g.MessageCount)},
		{label: t("field.group.recipientCount", nil), value: strconv.Itoa(g.RecipientCount)},
		{label: t("field.group.ipCount", nil), value: strconv.Itoa(g.RemoteIPCount)},
		{label: t("field.group.firstSeen", nil), value: stamp(g.FirstSeen)},
		{label: t("field.group.lastSeen", nil), value: stamp(g.LastSeen)},
		{label: t("field.group.stateUpdated", nil), value: stamp(g.StateUpdatedAt)},
		{label: t("field.group.diagnosticTemplate", nil), value: dash(g.DiagnosticTemplate), kind: fieldCodeBlock, wide: true},
		{label: t("field.group.key", nil), value: g.GroupKey, kind: fieldCode, wide: true},
	})
	d.space(5)

	// Statistics.
	stats := rep.Stats
	if stats == nil {
		stats = &models.GroupBounceStats{}
	}
	d.heading(t("page.groupDetail.stats", nil), 20)
	for i, s := range []struct {
		key    string
		values []string
	}{
		{"field.group.recipients", stats.Recipients},
		{"field.group.ips", stats.RemoteIPs},
		{"field.common.remoteMta", stats.RemoteMTAs},
	} {
		if i > 0 {
			d.space(5)
		}
		d.valueList(t(s.key, nil), s.values)
	}
	return d.pdf.Output(w)
}

// groupBadges are the status badges of the overview, as the group detail of
// the Web UI shows them.
func groupBadges(g *models.BounceGroup, a Analysis, t func(string, map[string]any) string, tr Translate) []badge {
	category, ok := tr("value.category."+g.Category+".label", nil)
	if !ok {
		category = g.Category
	}
	list := []badge{{category, categoryTones[g.Category]}}
	if g.Actionable {
		list = append(list, badge{t("value.groupScope.actionable", nil), toneAccent})
	} else {
		list = append(list, badge{t("value.groupScope.excluded", nil), toneNeutral})
	}
	// An open recipient-side group is on the excluded list, which has no states.
	if g.Actionable || g.State != "open" {
		list = append(list, badge{t("value.groupState."+g.State, nil), stateTones[g.State]})
	}
	if g.Actionable {
		if tn, known := severityTones[a.Severity]; known {
			list = append(list, badge{t("value.severity."+a.Severity, nil), tn})
		} else {
			list = append(list, badge{t("value.severity.unknown", nil), toneNeutral})
		}
	}
	if a.NeedsReview {
		list = append(list, badge{t("value.reportConfidence.low", nil), toneWarning})
	}
	switch g.Responsible {
	case "sender", "recipient", "domain":
		list = append(list, badge{t("value.responsible."+g.Responsible, nil), toneAccent})
	default:
		list = append(list, badge{t("value.responsible.unknown", nil), toneNeutral})
	}
	if a.Unanalyzable && a.Status != "running" {
		list = append(list, badge{t("value.reportStatus.unanalyzable", nil), toneDanger})
	}
	if g.Actionable && g.NeedsAnalysis && a.Status != "running" {
		list = append(list, badge{t("field.group.needsAnalysis", nil), toneWarning})
	}
	return list
}

// formatTime renders a date and time like the Web UI does for the language
// ("2026/09/17 14:05" in Japanese, "09/17/2026, 14:05" in English).
func formatTime(tm time.Time, loc *time.Location, language string) string {
	if loc == nil {
		loc = time.Local
	}
	if language == "en" {
		return tm.In(loc).Format("01/02/2006, 15:04")
	}
	return tm.In(loc).Format("2006/01/02 15:04")
}

// Package pdfreport renders the PDF reports of MailCare (the report of a
// group, WriteGroupReport) on the server: A4 portrait, the Japanese font
// BIZ UDPGothic (embedded/fonts/, SIL Open Font License 1.1, see
// embedded/fonts/OFL.txt) embedded as a subset of the glyphs used, Go Mono
// for codes, and the light colors of the Web UI.
package pdfreport

import (
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"codeberg.org/go-pdf/fpdf"
	"golang.org/x/image/font/gofont/gomono"

	"mailcare/app/modules"
)

// The text font files in modules.EmbeddedFS (regular and bold).
const (
	fontRegularFile = "fonts/BIZUDPGothic-Regular.ttf"
	fontBoldFile    = "fonts/BIZUDPGothic-Bold.ttf"
)

// textFonts reads the text fonts (regular, bold) once.
var textFonts = sync.OnceValues(func() ([2][]byte, error) {
	var out [2][]byte
	if modules.EmbeddedFS == nil {
		return out, fmt.Errorf("pdfreport: the embedded fonts are not available")
	}
	for i, name := range []string{fontRegularFile, fontBoldFile} {
		data, err := fs.ReadFile(modules.EmbeddedFS, name)
		if err != nil {
			return out, fmt.Errorf("pdfreport: %w", err)
		}
		out[i] = data
	}
	return out, nil
})

const (
	familyText = "text"
	familyCode = "code"
	// codeScale shrinks Go Mono, whose letters look larger than the text
	// font's at the same size.
	codeScale = 0.92
)

// Page geometry in millimeters (A4 portrait). The footer sits inside the
// bottom margin.
const (
	marginX      = 18.0
	marginTop    = 16.0
	marginBottom = 20.0
	footerOffset = 12.0 // from the bottom edge to the footer line
	ptToMM       = 25.4 / 72
	lineSpacing  = 1.5 // line height / font size
)

// rgb is a color of the Web UI's light theme (frontend/tailwind.config.js).
type rgb struct{ r, g, b int }

func hexColor(s string) rgb {
	v, _ := strconv.ParseUint(strings.TrimPrefix(s, "#"), 16, 32)
	return rgb{int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff)}
}

var (
	colorInk    = hexColor("#0f172a")
	colorMuted  = hexColor("#64748b")
	colorLine   = hexColor("#e2e8f0")
	colorWell   = hexColor("#f1f5f9")
	colorAccent = hexColor("#4f46e5")
)

// tone is the color set of a badge (the tones of the Web UI's Badge).
type tone int

const (
	toneNeutral tone = iota
	toneSuccess
	toneWarning
	toneDanger
	toneInfo
	toneAccent
)

// toneColors: text, background and border of each tone.
var toneColors = map[tone][3]rgb{
	toneNeutral: {colorInk, colorWell, colorLine},
	toneSuccess: {hexColor("#15803d"), hexColor("#dcfce7"), hexColor("#b9e4c6")},
	toneWarning: {hexColor("#a16207"), hexColor("#fef9c3"), hexColor("#ead7a4")},
	toneDanger:  {hexColor("#b91c1c"), hexColor("#fee2e2"), hexColor("#f0b7b7")},
	toneInfo:    {hexColor("#1d4ed8"), hexColor("#dbeafe"), hexColor("#b3c8f2")},
	toneAccent:  {hexColor("#4f46e5"), hexColor("#eef2ff"), hexColor("#c5c3f5")},
}

// document is an A4 page flow: the drawing helpers advance the current
// position and start a new page when what they draw does not fit above the
// bottom margin.
type document struct {
	pdf    *fpdf.Fpdf
	width  float64 // content width
	footer string  // left part of the footer of every page
	widths map[runeFont]float64
	family string
	style  string
	size   float64
}

type runeFont struct {
	family, style string
	size          float64
	r             rune
}

func newDocument(footer string) (*document, error) {
	fonts, err := textFonts()
	if err != nil {
		return nil, err
	}
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(marginX, marginTop, marginX)
	pdf.SetAutoPageBreak(false, marginBottom)
	pdf.SetCellMargin(0)
	pdf.AddUTF8FontFromBytes(familyText, "", fonts[0])
	pdf.AddUTF8FontFromBytes(familyText, "B", fonts[1])
	pdf.AddUTF8FontFromBytes(familyCode, "", gomono.TTF)
	pw, _ := pdf.GetPageSize()
	d := &document{pdf: pdf, width: pw - 2*marginX, footer: footer, widths: map[runeFont]float64{}}
	pdf.AliasNbPages("")
	pdf.SetFooterFunc(d.drawFooter)
	pdf.AddPage()
	return d, nil
}

// drawFooter draws a hairline, the footer text and "page / pages".
func (d *document) drawFooter() {
	pw, ph := d.pdf.GetPageSize()
	y := ph - footerOffset
	d.setDrawColor(colorLine)
	d.pdf.SetLineWidth(0.2)
	d.pdf.Line(marginX, y, pw-marginX, y)
	d.setTextColor(colorMuted)
	d.setFont(familyText, "", 8)
	d.pdf.SetXY(marginX, y+1.5)
	d.pdf.CellFormat(d.width*0.75, 4, d.fit(d.footer, d.width*0.75), "", 0, "L", false, 0, "")
	// The page count alias is replaced in a core font only.
	d.pdf.SetFont("Helvetica", "", 8)
	d.pdf.SetXY(marginX+d.width*0.75, y+1.5)
	d.pdf.CellFormat(d.width*0.25, 4, strconv.Itoa(d.pdf.PageNo())+" / {nb}", "", 0, "R", false, 0, "")
}

func (d *document) setFont(family, style string, size float64) {
	d.family, d.style, d.size = family, style, size
	d.pdf.SetFont(family, style, size)
}

// setCodeFont selects the code font when Go Mono can draw s (ASCII only),
// else the text font.
func (d *document) setCodeFont(s string, size float64) {
	if isASCII(s) {
		d.setFont(familyCode, "", size*codeScale)
		return
	}
	d.setFont(familyText, "", size)
}

func (d *document) setTextColor(c rgb) { d.pdf.SetTextColor(c.r, c.g, c.b) }
func (d *document) setFillColor(c rgb) { d.pdf.SetFillColor(c.r, c.g, c.b) }
func (d *document) setDrawColor(c rgb) { d.pdf.SetDrawColor(c.r, c.g, c.b) }

// lineHeight is the line height of a font size in points.
func lineHeight(size float64) float64 {
	return size * ptToMM * lineSpacing
}

// bottom is the lowest y content may reach on a page.
func (d *document) bottom() float64 {
	_, ph := d.pdf.GetPageSize()
	return ph - marginBottom
}

// ensure starts a new page unless h more millimeters fit on this one (a
// block taller than a whole page is started where it is).
func (d *document) ensure(h float64) {
	y := d.pdf.GetY()
	if y+h > d.bottom() && y > marginTop+0.01 {
		d.pdf.AddPage()
	}
}

// space moves the position down by h (not past the bottom of the page).
func (d *document) space(h float64) {
	d.pdf.SetY(min(d.pdf.GetY()+h, d.bottom()))
}

// runeWidth is the width of a rune in the current font.
func (d *document) runeWidth(r rune) float64 {
	k := runeFont{d.family, d.style, d.size, r}
	if w, ok := d.widths[k]; ok {
		return w
	}
	w := d.pdf.GetStringWidth(string(r))
	d.widths[k] = w
	return w
}

// textWidth is the width of a string in the current font.
func (d *document) textWidth(s string) float64 {
	var w float64
	for _, r := range s {
		w += d.runeWidth(r)
	}
	return w
}

// fit shortens s with an ellipsis to fit width w in the current font.
func (d *document) fit(s string, w float64) string {
	if d.textWidth(s) <= w {
		return s
	}
	rs := []rune(s)
	for len(rs) > 0 && d.textWidth(string(rs))+d.runeWidth('…') > w {
		rs = rs[:len(rs)-1]
	}
	return string(rs) + "…"
}

// wrap breaks text into lines no wider than w in the current font: at
// line feeds, after spaces, and between two characters when one of them is
// Japanese (keeping closing punctuation and small kana off the start of a
// line and opening brackets off its end). A word wider than a line is
// broken where it overflows.
func (d *document) wrap(text string, w float64) []string {
	var out []string
	for para := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		out = append(out, d.wrapParagraph([]rune(para), w)...)
	}
	return out
}

func (d *document) wrapParagraph(rs []rune, w float64) []string {
	if len(rs) == 0 {
		return []string{""}
	}
	var lines []string
	start := 0
	for start < len(rs) {
		width, next, i := 0.0, -1, start
		for ; i < len(rs); i++ {
			cw := d.runeWidth(rs[i])
			if width+cw > w && i > start {
				break
			}
			width += cw
			if i+1 < len(rs) && canBreakBetween(rs[i], rs[i+1]) {
				next = i + 1
			}
		}
		if i == len(rs) {
			lines = append(lines, strings.TrimRight(string(rs[start:]), " "))
			break
		}
		end := i
		if next > start {
			end = next
		}
		lines = append(lines, strings.TrimRight(string(rs[start:end]), " "))
		start = end
		for start < len(rs) && rs[start] == ' ' {
			start++
		}
	}
	return lines
}

// Japanese line breaking (a basic subset of JIS X 4051).
const (
	noLineStart = "、。，．・：；？！ー）］｝」』】〕〉》’”ぁぃぅぇぉっゃゅょゎァィゥェォッャュョヮヵヶ々ゝゞヽヾ〜…‥,.)]}!?:;"
	noLineEnd   = "（［｛「『【〔〈《‘“([{"
)

func canBreakBetween(a, b rune) bool {
	if a == ' ' {
		return b != ' '
	}
	if strings.ContainsRune(noLineStart, b) || strings.ContainsRune(noLineEnd, a) {
		return false
	}
	return isWide(a) || isWide(b)
}

// isWide reports whether r is a Japanese character (between two of which a
// line may break).
func isWide(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) ||
		(r >= 0x3000 && r <= 0x303f) || (r >= 0xff00 && r <= 0xffef)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// lines draws text lines at x from the current position, one per lh, in
// the current font and text color, starting a new page for a line that does
// not fit.
func (d *document) lines(lines []string, x, w, lh float64) {
	for _, l := range lines {
		d.ensure(lh)
		d.pdf.SetXY(x, d.pdf.GetY())
		d.pdf.CellFormat(w, lh, l, "", 2, "L", false, 0, "")
	}
}

// box draws lines on a filled box with padding pad (and a colored bar on
// its left when bar is set). A box that does not fit on this page moves to
// the next one; one taller than a page is split at the page breaks. font
// selects the font of the lines (again on every new page, since the footer
// changes it).
func (d *document) box(lines []string, x, w, lh, pad float64, fill rgb, bar *rgb, font func()) {
	full := float64(len(lines))*lh + 2*pad
	d.ensure(full)
	for len(lines) > 0 {
		room := int((d.bottom() - d.pdf.GetY() - 2*pad) / lh)
		if room < 1 {
			d.pdf.AddPage()
			continue
		}
		n := min(room, len(lines))
		y := d.pdf.GetY()
		h := float64(n)*lh + 2*pad
		d.setFillColor(fill)
		d.pdf.RoundedRect(x, y, w, h, 1.5, "1234", "F")
		textX := x + pad
		if bar != nil {
			d.setFillColor(*bar)
			d.pdf.Rect(x, y, 1.2, h, "F")
			textX += 1.2
		}
		font()
		d.pdf.SetY(y + pad)
		d.lines(lines[:n], textX, w-(textX-x)-pad, lh)
		d.pdf.SetY(y + h)
		lines = lines[n:]
		if len(lines) > 0 {
			d.pdf.AddPage()
		}
	}
}

// heading draws a section heading: an accent bar, the bold title and a rule
// under it. It moves to a new page together with the first minNext
// millimeters of what follows.
func (d *document) heading(title string, minNext float64) {
	const size = 12.5
	lh := lineHeight(size)
	d.ensure(lh + 4 + minNext)
	y := d.pdf.GetY()
	d.setFillColor(colorAccent)
	d.pdf.Rect(marginX, y+lh*0.2, 1.2, lh*0.6, "F")
	d.setFont(familyText, "B", size)
	d.setTextColor(colorInk)
	d.pdf.SetXY(marginX+3.5, y)
	d.pdf.CellFormat(d.width-3.5, lh, d.fit(title, d.width-3.5), "", 2, "L", false, 0, "")
	d.setDrawColor(colorLine)
	d.pdf.SetLineWidth(0.3)
	d.pdf.Line(marginX, y+lh+1, marginX+d.width, y+lh+1)
	d.pdf.SetY(y + lh + 4)
}

// badge is one status chip.
type badge struct {
	text string
	tone tone
}

// badges draws a row of rounded chips, wrapping to the next row when a chip
// does not fit.
func (d *document) badges(list []badge) {
	const size, padX, h, gap = 8.5, 3.0, 6.2, 2.0
	d.ensure(h)
	x, y := marginX, d.pdf.GetY()
	for _, b := range list {
		d.setFont(familyText, "", size)
		text := d.fit(b.text, d.width-2*padX)
		w := d.textWidth(text) + 2*padX
		if x > marginX && x+w > marginX+d.width {
			x, y = marginX, y+h+gap
			d.pdf.SetY(y)
			d.ensure(h)
			y = d.pdf.GetY()
			d.setFont(familyText, "", size)
		}
		c := toneColors[b.tone]
		d.setFillColor(c[1])
		d.setDrawColor(c[2])
		d.pdf.SetLineWidth(0.25)
		d.pdf.RoundedRect(x, y, w, h, h/2, "1234", "FD")
		d.setTextColor(c[0])
		d.pdf.SetXY(x+padX, y)
		d.pdf.CellFormat(w-2*padX, h, text, "", 0, "L", false, 0, "")
		x += w + gap
	}
	d.pdf.SetY(y + h)
}

// fieldKind is how the value of a field is drawn.
type fieldKind int

const (
	fieldText      fieldKind = iota // text font
	fieldCode                       // code font, no box
	fieldCodeBlock                  // code font on a box, full width
)

// field is one item of the overview grid.
type field struct {
	label string
	value string
	kind  fieldKind
	wide  bool // spans both columns
}

// Sizes of the overview grid.
const (
	fieldLabelSize = 8.5
	fieldValueSize = 10.5
	fieldGapX      = 8.0
	fieldGapY      = 3.2
	codePad        = 2.2
)

// fields draws the items as a two-column grid (wide items take a row of
// their own), each a muted label over its value.
func (d *document) fields(items []field) {
	col := (d.width - fieldGapX) / 2
	for i := 0; i < len(items); {
		row := []field{items[i]}
		if !items[i].wide && i+1 < len(items) && !items[i+1].wide {
			row = append(row, items[i+1])
		}
		i += len(row)
		w := col
		if len(row) == 1 && row[0].wide {
			w = d.width
		}
		// A block value draws itself (it may span pages); plain values
		// are laid out in the row.
		if row[0].kind == fieldCodeBlock {
			d.fieldLabel(row[0].label, marginX, w)
			lines, font := d.valueLines(row[0].value, fieldCodeBlock, w-2*codePad)
			d.box(lines, marginX, w, lineHeight(fieldValueSize*0.95), codePad, colorWell, nil, font)
			d.space(fieldGapY)
			continue
		}
		height := 0.0
		wrapped := make([][]string, len(row))
		fonts := make([]func(), len(row))
		for j, f := range row {
			wrapped[j], fonts[j] = d.valueLines(f.value, f.kind, w)
			height = max(height, lineHeight(fieldLabelSize)+float64(len(wrapped[j]))*lineHeight(fieldValueSize))
		}
		d.ensure(height)
		y := d.pdf.GetY()
		for j, f := range row {
			x := marginX + float64(j)*(col+fieldGapX)
			d.pdf.SetY(y)
			d.fieldLabel(f.label, x, w)
			fonts[j]()
			d.lines(wrapped[j], x, w, lineHeight(fieldValueSize))
		}
		d.pdf.SetY(y + height + fieldGapY)
	}
}

func (d *document) fieldLabel(label string, x, w float64) {
	lh := lineHeight(fieldLabelSize)
	d.ensure(lh * 2)
	d.setFont(familyText, "", fieldLabelSize)
	d.setTextColor(colorMuted)
	d.pdf.SetXY(x, d.pdf.GetY())
	d.pdf.CellFormat(w, lh, d.fit(label, w), "", 2, "L", false, 0, "")
}

// valueLines wraps a value in its font and returns the lines with the
// function that selects that font and color again.
func (d *document) valueLines(value string, kind fieldKind, w float64) ([]string, func()) {
	size := fieldValueSize
	if kind == fieldCodeBlock {
		size *= 0.95
	}
	font := func() {
		if kind == fieldText {
			d.setFont(familyText, "", size)
		} else {
			d.setCodeFont(value, size)
		}
		d.setTextColor(colorInk)
	}
	font()
	return d.wrap(value, w), font
}

// valueList draws a titled list of values (a statistic) in two columns,
// every value on a row with a hairline under it; "-" when there is none.
func (d *document) valueList(title string, values []string) {
	const titleSize, valueSize, pad = 10.5, 9.5, 1.3
	lhTitle, lh := lineHeight(titleSize), lineHeight(valueSize)
	d.ensure(lhTitle + 2 + lh + 2*pad)
	y := d.pdf.GetY()
	d.setFont(familyText, "B", titleSize)
	d.setTextColor(colorInk)
	d.pdf.SetXY(marginX, y)
	d.pdf.CellFormat(d.textWidth(title), lhTitle, title, "", 0, "L", false, 0, "")
	d.setFont(familyText, "", titleSize)
	d.setTextColor(colorMuted)
	d.pdf.CellFormat(0, lhTitle, " ("+strconv.Itoa(len(values))+")", "", 2, "L", false, 0, "")
	d.pdf.SetY(y + lhTitle + 1.5)
	if len(values) == 0 {
		d.setFont(familyText, "", valueSize)
		d.lines([]string{"-"}, marginX, d.width, lh)
		return
	}
	col := (d.width - fieldGapX) / 2
	for i := 0; i < len(values); i += 2 {
		row := values[i:min(i+2, len(values))]
		wrapped := make([][]string, len(row))
		height := 0.0
		for j, v := range row {
			d.setCodeFont(v, valueSize)
			wrapped[j] = d.wrap(v, col)
			height = max(height, float64(len(wrapped[j]))*lh+2*pad)
		}
		d.ensure(height)
		y := d.pdf.GetY()
		for j, v := range row {
			x := marginX + float64(j)*(col+fieldGapX)
			d.setCodeFont(v, valueSize)
			d.setTextColor(colorInk)
			d.pdf.SetY(y + pad)
			d.lines(wrapped[j], x, col, lh)
			d.setDrawColor(colorLine)
			d.pdf.SetLineWidth(0.2)
			d.pdf.Line(x, y+height, x+col, y+height)
		}
		d.pdf.SetY(y + height)
	}
}

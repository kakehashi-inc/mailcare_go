package mailengine

import (
	"bytes"
	"io"
	"mime"
	netmail "net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/unicode"
)

// Charset handling (design 5.2). go-message resolves IANA / HTML charset
// names, but Japanese mail often labels bodies and encoded words with
// aliases (cp932, x-sjis, iso-2022-jp-ms, eucjp, ...), omits the charset or
// mislabels it (us-ascii for Shift_JIS bytes). This file installs a
// CharsetReader that
//
//  1. maps the aliases of charsetAliases to their decoder before decoding;
//  2. when the charset is missing, unknown, or its decoder produces
//     replacement runes, detects the encoding from the bytes: valid UTF-8 is
//     kept, a UTF-8 / UTF-16 BOM is honoured, ISO-2022-JP escape sequences
//     select ISO-2022-JP, then Shift_JIS and EUC-JP are tried and the first
//     clean decode wins; otherwise the raw bytes are kept.
//
// The same function serves the bodies, the RFC 2047 encoded words of the
// headers (mime.WordDecoder) and, for HTML without a MIME charset, the
// <meta charset> of the document.

// charsetAliases maps lower-cased charset labels that ianaindex / htmlindex
// do not know (or map badly) to their decoder. Labels not listed here go
// through go-message's own resolution (IANA names, then "cs" + name, then
// the HTML index).
var charsetAliases = map[string]encoding.Encoding{
	// Shift_JIS family: x/text's ShiftJIS is the WHATWG decoder, i.e. the
	// Windows-31J (cp932) superset.
	"shift_jis": japanese.ShiftJIS, "shift-jis": japanese.ShiftJIS, "shiftjis": japanese.ShiftJIS,
	"sjis": japanese.ShiftJIS, "x-sjis": japanese.ShiftJIS, "cp932": japanese.ShiftJIS, "ms932": japanese.ShiftJIS,
	"windows-31j": japanese.ShiftJIS, "ms_kanji": japanese.ShiftJIS, "csshiftjis": japanese.ShiftJIS,
	"cswindows31j": japanese.ShiftJIS,
	// ISO-2022-JP family.
	"iso-2022-jp": japanese.ISO2022JP, "iso2022jp": japanese.ISO2022JP, "csiso2022jp": japanese.ISO2022JP,
	"iso-2022-jp-ms": japanese.ISO2022JP, "iso-2022-jp-1": japanese.ISO2022JP, "iso-2022-jp-2": japanese.ISO2022JP,
	"iso-2022-jp-3": japanese.ISO2022JP, "jis": japanese.ISO2022JP, "x-iso2022jp": japanese.ISO2022JP,
	// EUC-JP family.
	"euc-jp": japanese.EUCJP, "eucjp": japanese.EUCJP, "euc_jp": japanese.EUCJP, "x-euc-jp": japanese.EUCJP,
	"ujis": japanese.EUCJP, "cseucpkdfmtjapanese": japanese.EUCJP, "x-eucjp": japanese.EUCJP,
	// Unicode.
	"utf-8": unicode.UTF8, "utf8": unicode.UTF8, "utf-8n": unicode.UTF8, "utf_8": unicode.UTF8,
	"utf-16": unicode.UTF16(unicode.BigEndian, unicode.UseBOM), "utf16": unicode.UTF16(unicode.BigEndian, unicode.UseBOM),
	"utf-16be": unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM), "utf-16le": unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM),
	// Western.
	"latin1": charmap.ISO8859_1, "latin-1": charmap.ISO8859_1, "iso8859-1": charmap.ISO8859_1,
	"iso-8859-1": charmap.ISO8859_1, "iso_8859-1": charmap.ISO8859_1, "l1": charmap.ISO8859_1,
	"cp1252": charmap.Windows1252, "windows-1252": charmap.Windows1252, "win-1252": charmap.Windows1252,
	"ms-ansi": charmap.Windows1252, "iso-8859-15": charmap.ISO8859_15, "latin9": charmap.ISO8859_15,
}

// asciiLabels are labels that promise 7-bit content; bytes above 0x7f under
// such a label are mislabeled and go through detection.
var asciiLabels = map[string]bool{"us-ascii": true, "ascii": true, "ansi_x3.4-1968": true, "iso646-us": true, "csascii": true,
	mislabeledASCIILabel: true}

func init() {
	// Importing go-message/charset (parse.go) set message.CharsetReader to
	// its resolver; replace it with ours, which falls back to it.
	message.CharsetReader = charsetReader
}

// charsetReader is the message.CharsetReader of the engine: it reads the
// whole input and decodes it with
// decodeBytes.
func charsetReader(label string, input io.Reader) (io.Reader, error) {
	data, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(decodeBytes(label, data)), nil
}

// normalizeCharsetLabel lower-cases and trims a charset label.
func normalizeCharsetLabel(label string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(label), `"'`))
}

// decodeBytes converts data from the labeled charset to UTF-8. An empty or
// ASCII label, an unknown label or a decode that produced replacement runes
// hands the bytes to detectDecode; when detection finds nothing the labeled
// decode is kept (or the raw bytes when there was none).
func decodeBytes(label string, data []byte) []byte {
	label = normalizeCharsetLabel(label)
	if label == "" || asciiLabels[label] {
		if out, ok := detectDecode(data); ok {
			return out
		}
		return data
	}
	decoded, ok := decodeWith(label, data)
	if ok && !bytes.ContainsRune(decoded, utf8.RuneError) {
		return decoded
	}
	if out, ok := detectDecode(data); ok {
		return out
	}
	if ok {
		return decoded
	}
	return data
}

// decodeWith decodes data with the decoder of a known label (alias table
// first, then go-message's resolver). ok is false for an unknown label.
func decodeWith(label string, data []byte) ([]byte, bool) {
	if enc, found := charsetAliases[label]; found {
		out, err := enc.NewDecoder().Bytes(data)
		if err != nil {
			return nil, false
		}
		return out, true
	}
	r, err := charset.Reader(label, bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, false
	}
	return out, true
}

var (
	utf8BOM    = []byte{0xef, 0xbb, 0xbf}
	utf16BEBOM = []byte{0xfe, 0xff}
	utf16LEBOM = []byte{0xff, 0xfe}
	// ESC $ B / ESC $ @ (JIS X 0208) and ESC ( B / ESC ( J (back to ASCII / JIS Roman).
	iso2022EscapeRe = regexp.MustCompile(`\x1b\$[@B]|\x1b\([BJ]`)
)

// detectDecode guesses the encoding of unlabeled or mislabeled bytes and
// returns the UTF-8 text. ok is false when no clean decoding was found (the
// caller then keeps the raw bytes).
func detectDecode(data []byte) ([]byte, bool) {
	switch {
	case bytes.HasPrefix(data, utf8BOM):
		data = data[len(utf8BOM):]
		if utf8.Valid(data) {
			return data, true
		}
	case bytes.HasPrefix(data, utf16BEBOM), bytes.HasPrefix(data, utf16LEBOM):
		out, err := unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewDecoder().Bytes(data)
		if err == nil && cleanText(out) {
			return out, true
		}
	}
	// ISO-2022-JP is 7-bit and therefore valid UTF-8: its escape sequences
	// must be checked before the plain UTF-8 test.
	if iso2022EscapeRe.Match(data) {
		if out, err := japanese.ISO2022JP.NewDecoder().Bytes(data); err == nil && cleanText(out) {
			return out, true
		}
	}
	if utf8.Valid(data) {
		return data, true
	}
	// Shift_JIS and EUC-JP overlap: EUC-JP bytes decode as Shift_JIS into
	// long runs of half-width katakana, so when both decode cleanly the
	// candidate with fewer half-width katakana wins.
	var best []byte
	bestScore := -1
	for _, enc := range []encoding.Encoding{japanese.ShiftJIS, japanese.EUCJP} {
		out, err := enc.NewDecoder().Bytes(data)
		if err != nil || !cleanText(out) {
			continue
		}
		score := halfWidthKatakana(out)
		if best == nil || score < bestScore {
			best, bestScore = out, score
		}
	}
	if best != nil {
		return best, true
	}
	return nil, false
}

// cleanText reports whether decoded text is valid UTF-8 without replacement
// runes or control garbage (C0 controls other than tab / LF / CR / FF / ESC
// and the C1 range).
func cleanText(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		switch {
		case r == utf8.RuneError:
			return false
		case r < 0x20 && r != '\t' && r != '\n' && r != '\r' && r != '\f' && r != 0x1b:
			return false
		case r >= 0x80 && r <= 0x9f:
			return false
		}
	}
	return true
}

// halfWidthKatakana counts the half-width katakana runes (U+FF61..U+FF9F).
func halfWidthKatakana(b []byte) int {
	n := 0
	for _, r := range string(b) {
		if r >= 0xff61 && r <= 0xff9f {
			n++
		}
	}
	return n
}

// htmlMetaCharsetRe finds <meta charset="..."> or
// <meta http-equiv="Content-Type" content="text/html; charset=..."> in the
// head of an HTML document.
var htmlMetaCharsetRe = regexp.MustCompile(`(?i)<meta[^>]+charset\s*=\s*["']?\s*([a-z0-9._:\-]+)`)

// decodeBodyBytes converts the bytes of a text part that go-message did not
// decode: no charset parameter, or an ASCII label. HTML documents are first
// asked for their <meta charset>. Parts with another label were already
// decoded through charsetReader and are returned as they are.
func decodeBodyBytes(data []byte, label string, isHTML bool) []byte {
	label = normalizeCharsetLabel(label)
	if label != "" && !asciiLabels[label] {
		return data
	}
	if isHTML {
		head := data
		if len(head) > 4096 {
			head = head[:4096]
		}
		if m := htmlMetaCharsetRe.FindSubmatch(head); m != nil {
			return decodeBytes(string(m[1]), data)
		}
	}
	return decodeBytes("", data)
}

// asciiWordLabelRe matches the charset label of an RFC 2047 encoded word
// that mime.WordDecoder decodes by itself (US-ASCII), which would turn
// mislabeled Japanese bytes into replacement runes before our reader sees
// them. decodeHeaderWords rewrites the label so that the bytes reach
// charsetReader and its detection.
var asciiWordLabelRe = regexp.MustCompile(`(?i)=\?(?:us-ascii|ascii|ansi_x3\.4-1968)(\*[^?]*)?\?([bq])\?`)

// mislabeledASCIILabel is the private label the rewrite uses.
const mislabeledASCIILabel = "x-mailcare-ascii"

// decodeHeaderWords decodes the RFC 2047 encoded words of a raw header value
// through charsetReader and repairs raw 8-bit values. On a malformed value
// the input is returned (repaired as far as possible).
func decodeHeaderWords(raw string) string {
	raw = asciiWordLabelRe.ReplaceAllString(raw, "=?"+mislabeledASCIILabel+"$1?$2?")
	dec := mime.WordDecoder{CharsetReader: charsetReader}
	if out, err := dec.DecodeHeader(raw); err == nil {
		return decodeHeaderText(out)
	}
	return decodeHeaderText(raw)
}

// headerAddressParser parses address headers with the same decoding.
var headerAddressParser = netmail.AddressParser{WordDecoder: &mime.WordDecoder{CharsetReader: charsetReader}}

// decodeHeaderText repairs a header value that is not valid UTF-8 (raw
// 8-bit bytes without encoded words) or raw ISO-2022-JP by detecting its
// encoding.
func decodeHeaderText(s string) string {
	// Raw ISO-2022-JP is 7-bit (valid UTF-8) and recognised by its escapes.
	if utf8.ValidString(s) && !iso2022EscapeRe.MatchString(s) {
		return s
	}
	if out, ok := detectDecode([]byte(s)); ok {
		return string(out)
	}
	return s
}

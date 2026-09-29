package mailengine

import (
	"bufio"
	"bytes"
	"strings"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/textproto"
)

// HeaderText renders the top-level header block of a raw message as text:
// one "Name: value" line per field in the order of the message, folded
// lines joined and encoded words decoded to UTF-8 (a value that cannot be
// decoded is kept as it is). A header block the MIME reader cannot read is
// returned as it is, with the line endings normalized to "\n".
func HeaderText(raw []byte) string {
	h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		head, _ := splitHeaderBlock(raw)
		return normalizeText(head) + "\n"
	}
	mh := message.Header{Header: h}
	var b strings.Builder
	fields := mh.Fields()
	for fields.Next() {
		val, err := fields.Text()
		if err != nil {
			val = fields.Value()
		}
		b.WriteString(fields.Key())
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(val))
		b.WriteByte('\n')
	}
	return b.String()
}

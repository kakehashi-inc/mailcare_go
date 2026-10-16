package mailengine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"

	"mailcare/app/models"
)

// Delivery probe (app/modules/probe.go): the probe mails sent from the
// monitored mailboxes are looked for on the probe server. The probe mailbox
// may hold other mail too (a dedicated one is recommended, not required),
// so every step is limited to the probe mails and leaves the rest alone:
//
//   - One listing per check: the server is asked once for the subjects that
//     carry the mark of the probe subject form (UID SEARCH SUBJECT "<mark>",
//     the whole folder, every identifier), and the messages found are read
//     once. Only SUBJECT is used: some servers answer nothing for a search
//     on a custom header. Only UID, INTERNALDATE, ENVELOPE and the Subject
//     and probe header fields are read, with BODY.PEEK, so no flag (\Seen)
//     changes.
//   - A message counts as a probe mail only when its subject carries
//     "[<identifier>:<token>]" (anywhere: a relay may put "[SPAM]" in
//     front; the raw Subject field, then its RFC 2047 decoding, then the
//     subject of the envelope are tried) and its probe header carries
//     exactly that token as its single value: a reply or a forward that
//     quotes the subject has no such header.
//   - The caller decides which probe mails are deleted; exactly those UIDs
//     are deleted (UID EXPUNGE; on a server without UIDPLUS a plain EXPUNGE
//     only when no other message is flagged \Deleted, see expungeOnly).
//
// Nothing is fetched into an index.

// probeFetchBatch bounds how many messages one FETCH or deletion names.
const probeFetchBatch = 200

// ProbeMessage is one probe mail found on the probe server.
type ProbeMessage struct {
	UID        uint32
	Token      string
	Identifier string    // the identifier in front of the token in the subject
	From       string    // the sender address (lower case; "" when the envelope names none)
	ReceivedAt time.Time // INTERNALDATE (zero when the server gives none)
}

// ProbeRecorder records what was found on the probe server and returns the
// UIDs of the probe mails to delete there. An error leaves every message on
// the server.
type ProbeRecorder func(found []ProbeMessage) (remove map[uint32]bool, err error)

// probeSubjectRE finds "[<identifier>:<token>]" in a subject and captures
// the token.
var probeSubjectRE = regexp.MustCompile(`\[([A-Za-z0-9._-]{1,16}):([0-9a-f]{16})\]`)

// probeTokenRE is a token alone: 16 lower-case hex digits.
var probeTokenRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

// headerFields reads the fields of a fetched header section (unfolded),
// keyed by the lower-case field name, one value per occurrence.
func headerFields(raw []byte) map[string][]string {
	text := strings.NewReplacer("\r\n ", " ", "\r\n\t", " ", "\n ", " ", "\n\t", " ").Replace(string(raw))
	out := map[string][]string{}
	for _, line := range strings.Split(text, "\n") {
		name, value, ok := strings.Cut(strings.TrimRight(line, "\r"), ":")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(name))
		out[key] = append(out[key], strings.TrimSpace(value))
	}
	return out
}

// searchProbeUIDs lists the messages whose subject carries mark (the probe
// subject form; one UID SEARCH over the whole folder) and returns their
// UIDs, sorted.
func searchProbeUIDs(ctx context.Context, s *imapSession, mark string) ([]imap.UID, error) {
	criteria := imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: mark}}}
	result, err := s.client.UIDSearch(&criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search: %w", ctxErr(ctx, err))
	}
	uids := result.AllUIDs()
	slices.Sort(uids)
	return uids, nil
}

// probeMessageToken returns the identifier and the token of a message that
// is a probe mail: its probe header (named header) has a token as its single
// value and its subject carries "[<identifier>:<token>]" with that token;
// "" otherwise. The subject is the Subject field fetched (when there is one
// field), then its RFC 2047 decoding (a relay may re-encode the subject when
// it puts a non-ASCII tag in front), then the subject of the envelope.
func probeMessageToken(envelopeSubject string, section []byte, header string) (string, string) {
	fields := headerFields(section)
	values := fields[strings.ToLower(header)]
	if len(values) != 1 {
		return "", ""
	}
	token := values[0]
	if !probeTokenRE.MatchString(token) {
		return "", ""
	}
	var subjects []string
	if s := fields["subject"]; len(s) == 1 {
		subjects = append(subjects, s[0], decodeHeaderWords(s[0]))
	}
	subjects = append(subjects, envelopeSubject)
	for _, subject := range subjects {
		for _, m := range probeSubjectRE.FindAllStringSubmatch(subject, -1) {
			if m[2] == token {
				return m[1], token
			}
		}
	}
	return "", ""
}

// CheckProbeServer lists the probe mails of the folder of the probe server
// (the subjects that carry mark; see above), hands them to record (in UID
// order: the oldest first) and deletes the UIDs record returns. It returns
// the probe mails deleted. A failed deletion is returned with
// ErrProbeDelete wrapped, after record ran, with the probe mails of the
// batches deleted before it.
func CheckProbeServer(ctx context.Context, conn *models.Mailbox, password, header, mark string,
	record ProbeRecorder) (deleted []ProbeMessage, err error) {
	if conn == nil {
		return nil, errors.New("mailengine: probe server is nil")
	}
	if header == "" || mark == "" {
		return nil, errors.New("mailengine: probe header or subject mark is empty")
	}
	s, err := connect(ctx, conn, password)
	if err != nil {
		return nil, err
	}
	defer s.close(true)
	if _, err := s.selectFolder(ctx, conn.Folder); err != nil {
		return nil, err
	}
	uids, err := searchProbeUIDs(ctx, s, mark)
	if err != nil {
		return nil, err
	}
	var found []ProbeMessage
	section := &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: []string{"Subject", header},
		Peek:         true,
	}
	for start := 0; start < len(uids); start += probeFetchBatch {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+probeFetchBatch, len(uids))
		msgs, err := s.client.Fetch(imap.UIDSetNum(uids[start:end]...), &imap.FetchOptions{
			UID: true, InternalDate: true, Envelope: true, BodySection: []*imap.FetchItemBodySection{section},
		}).Collect()
		if err != nil {
			return nil, fmt.Errorf("fetch: %w", ctxErr(ctx, err))
		}
		for _, m := range msgs {
			if m.UID == 0 || m.Envelope == nil {
				continue
			}
			identifier, token := probeMessageToken(m.Envelope.Subject, m.FindBodySection(section), header)
			if token == "" {
				continue // not a probe mail (or not one that can be told for sure)
			}
			from := ""
			if len(m.Envelope.From) > 0 {
				from = strings.ToLower(m.Envelope.From[0].Addr())
			}
			found = append(found, ProbeMessage{UID: uint32(m.UID), Token: token, Identifier: identifier, From: from,
				ReceivedAt: m.InternalDate.UTC()})
		}
	}
	slices.SortFunc(found, func(a, b ProbeMessage) int { return cmp.Compare(a.UID, b.UID) })
	remove, err := record(found)
	if err != nil {
		return nil, err
	}
	var toDelete []ProbeMessage
	for _, m := range found {
		if remove[m.UID] {
			toDelete = append(toDelete, m)
		}
	}
	if len(toDelete) == 0 {
		return nil, nil
	}
	caps := s.client.Caps()
	uidExpunge := caps.Has(imap.CapUIDPlus) || caps.Has(imap.CapIMAP4rev2)
	for start := 0; start < len(toDelete); start += probeFetchBatch {
		batch := toDelete[start:min(start+probeFetchBatch, len(toDelete))]
		batchUIDs := make([]imap.UID, 0, len(batch))
		for _, m := range batch {
			batchUIDs = append(batchUIDs, imap.UID(m.UID))
		}
		var derr error
		if uidExpunge {
			derr = s.expungeUIDs(ctx, batchUIDs)
		} else {
			derr = s.expungeOnly(ctx, batchUIDs)
		}
		if derr != nil {
			return deleted, fmt.Errorf("%w: %w", ErrProbeDelete, derr)
		}
		deleted = append(deleted, batch...)
	}
	return deleted, nil
}

// ErrProbeDelete marks a failure to delete probe mails from the probe server
// (what was found was recorded; the next check tries again).
var ErrProbeDelete = errors.New("the probe mails could not be deleted from the probe server")

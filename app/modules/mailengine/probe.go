package mailengine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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
//   - The server is asked only for the messages whose subject carries the
//     marker (SEARCH HEADER Subject); only their envelope and the probe
//     header are read, with BODY.PEEK, so no flag (\Seen) changes.
//   - A message counts as a probe mail only when its subject and its probe
//     header carry the same token (marker followed by hex digits): a reply
//     or a forward that quotes the subject has no such header.
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
	From       string    // the sender address (lower case; "" when the envelope names none)
	ReceivedAt time.Time // INTERNALDATE
}

// ProbeRecorder records what was found on the probe server and returns the
// UIDs of the probe mails to delete there. An error leaves every message on
// the server.
type ProbeRecorder func(found []ProbeMessage) (remove map[uint32]bool, err error)

// CheckProbeServer searches the folder of the probe server for the probe
// mails (see above), hands them to record and deletes the UIDs record
// returns. It returns the number of messages deleted. A failed deletion is
// returned with ErrProbeDelete wrapped, after record ran.
func CheckProbeServer(ctx context.Context, conn *models.Mailbox, password, marker, header string, record ProbeRecorder) (int, error) {
	if conn == nil {
		return 0, errors.New("mailengine: probe server is nil")
	}
	if marker == "" || header == "" {
		return 0, errors.New("mailengine: probe marker or header is empty")
	}
	tokenRE, err := regexp.Compile(regexp.QuoteMeta(marker) + `[0-9A-Fa-f]{16}\b`)
	if err != nil {
		return 0, err
	}
	s, err := connect(ctx, conn, password)
	if err != nil {
		return 0, err
	}
	defer s.close(true)
	if _, err := s.selectFolder(ctx, conn.Folder); err != nil {
		return 0, err
	}
	criteria := &imap.SearchCriteria{
		Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: marker}},
	}
	search, err := s.client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return 0, fmt.Errorf("search: %w", ctxErr(ctx, err))
	}
	uids := search.AllUIDs()
	var found []ProbeMessage
	section := &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: []string{header},
		Peek:         true,
	}
	normalize := func(token string) string {
		return marker + strings.ToLower(strings.TrimPrefix(token, marker))
	}
	for start := 0; start < len(uids); start += probeFetchBatch {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		end := min(start+probeFetchBatch, len(uids))
		msgs, err := s.client.Fetch(imap.UIDSetNum(uids[start:end]...), &imap.FetchOptions{
			UID: true, InternalDate: true, Envelope: true, BodySection: []*imap.FetchItemBodySection{section},
		}).Collect()
		if err != nil {
			return 0, fmt.Errorf("fetch: %w", ctxErr(ctx, err))
		}
		for _, m := range msgs {
			if m.UID == 0 || m.Envelope == nil {
				continue
			}
			inSubject := map[string]bool{}
			for _, token := range tokenRE.FindAllString(m.Envelope.Subject, -1) {
				inSubject[normalize(token)] = true
			}
			headerTokens := tokenRE.FindAllString(string(m.FindBodySection(section)), -1)
			if len(headerTokens) != 1 || !inSubject[normalize(headerTokens[0])] {
				continue // not a probe mail of ours (or not one we can tell for sure)
			}
			from := ""
			if len(m.Envelope.From) > 0 {
				from = strings.ToLower(m.Envelope.From[0].Addr())
			}
			found = append(found, ProbeMessage{UID: uint32(m.UID), Token: normalize(headerTokens[0]), From: from,
				ReceivedAt: m.InternalDate.UTC()})
		}
	}
	remove, err := record(found)
	if err != nil {
		return 0, err
	}
	var toDelete []imap.UID
	for _, m := range found {
		if remove[m.UID] {
			toDelete = append(toDelete, imap.UID(m.UID))
		}
	}
	if len(toDelete) == 0 {
		return 0, nil
	}
	caps := s.client.Caps()
	uidExpunge := caps.Has(imap.CapUIDPlus) || caps.Has(imap.CapIMAP4rev2)
	deleted := 0
	for start := 0; start < len(toDelete); start += probeFetchBatch {
		batch := toDelete[start:min(start+probeFetchBatch, len(toDelete))]
		var err error
		if uidExpunge {
			err = s.expungeUIDs(ctx, batch)
		} else {
			err = s.expungeOnly(ctx, batch)
		}
		if err != nil {
			return deleted, fmt.Errorf("%w: %w", ErrProbeDelete, err)
		}
		deleted += len(batch)
	}
	return deleted, nil
}

// ErrProbeDelete marks a failure to delete probe mails from the probe server
// (what was found was recorded; the next check tries again).
var ErrProbeDelete = errors.New("the probe mails could not be deleted from the probe server")

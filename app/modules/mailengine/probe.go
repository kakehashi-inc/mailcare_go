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
// monitored mailboxes are looked for on the probe server. Each one carries a
// token (marker followed by hex digits) in its subject and in a header; the
// arrival check asks the server for the subjects that carry the marker,
// reads the token and the arrival time (INTERNALDATE) of every match without
// changing its flags (BODY.PEEK), lets the caller record the arrivals and
// then deletes from the server the messages whose token the caller names
// (the probe mailbox is reserved for the probe, so the confirmed mails are
// removed to keep it from filling up). Nothing is fetched into an index.

// probeFetchBatch bounds how many messages one FETCH or deletion names.
const probeFetchBatch = 200

// ProbeRecorder records the arrivals found on the probe server (token ->
// earliest INTERNALDATE) and returns the tokens whose messages may be
// deleted from the server. An error leaves every message on the server.
type ProbeRecorder func(found map[string]time.Time) (remove map[string]bool, err error)

// CheckProbeServer searches the folder of the probe server for the
// messages whose subject carries marker, reads their tokens (from the
// subject and from header) and arrival times, hands them to record and
// deletes the messages of the tokens record returns (\Deleted and UID
// EXPUNGE; on a server without UIDPLUS a plain EXPUNGE, used only when no
// other message is flagged \Deleted, see expungeOnly). It returns the
// number of messages deleted. A failed deletion is returned with
// ErrProbeDelete wrapped, after the arrivals were recorded.
func CheckProbeServer(ctx context.Context, conn *models.Mailbox, password, marker, header string, record ProbeRecorder) (int, error) {
	if conn == nil {
		return 0, errors.New("mailengine: probe server is nil")
	}
	if marker == "" {
		return 0, errors.New("mailengine: probe marker is empty")
	}
	tokenRE, err := regexp.Compile(regexp.QuoteMeta(marker) + `[0-9A-Fa-f]+`)
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
	found := map[string]time.Time{}
	tokensOf := map[imap.UID][]string{}
	section := &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: []string{header},
		Peek:         true,
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
			text := string(m.FindBodySection(section))
			if m.Envelope != nil {
				text = m.Envelope.Subject + "\n" + text
			}
			arrived := m.InternalDate.UTC()
			for _, token := range tokenRE.FindAllString(text, -1) {
				token = marker + strings.ToLower(strings.TrimPrefix(token, marker))
				if m.UID != 0 {
					tokensOf[m.UID] = append(tokensOf[m.UID], token)
				}
				if prev, ok := found[token]; !ok || (!arrived.IsZero() && arrived.Before(prev)) {
					found[token] = arrived
				}
			}
		}
	}
	remove, err := record(found)
	if err != nil {
		return 0, err
	}
	var toDelete []imap.UID
	for _, uid := range uids {
		for _, token := range tokensOf[uid] {
			if remove[token] {
				toDelete = append(toDelete, uid)
				break
			}
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

// ErrProbeDelete marks a failure to delete the confirmed probe mails from
// the probe server (the arrivals were recorded; the next check tries again).
var ErrProbeDelete = errors.New("the confirmed probe mails could not be deleted from the probe server")

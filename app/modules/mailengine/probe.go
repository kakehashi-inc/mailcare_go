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
// arrival check asks the server for the subjects that carry the marker and
// reads the token and the arrival time (INTERNALDATE) of every match. The
// probe server is only read: nothing is fetched into an index, no flag is
// changed (BODY.PEEK) and nothing is deleted.

// probeFetchBatch bounds how many messages one FETCH asks for.
const probeFetchBatch = 200

// FindProbeArrivals searches the folder of the probe server for the
// messages received on or after the day of since whose subject carries
// marker, and returns the arrival time of every token found (the earliest
// INTERNALDATE when a token arrived more than once). header names the header
// that carries the token too; it is read as well, so a match whose subject
// was rewritten on the way (and lost its token) is still recognized.
func FindProbeArrivals(ctx context.Context, conn *models.Mailbox, password string, since time.Time, marker, header string) (map[string]time.Time, error) {
	if conn == nil {
		return nil, errors.New("mailengine: probe server is nil")
	}
	if marker == "" {
		return nil, errors.New("mailengine: probe marker is empty")
	}
	tokenRE, err := regexp.Compile(regexp.QuoteMeta(marker) + `[0-9A-Fa-f]+`)
	if err != nil {
		return nil, err
	}
	s, err := connect(ctx, conn, password)
	if err != nil {
		return nil, err
	}
	defer s.close(true)
	if _, err := s.selectFolder(ctx, conn.Folder); err != nil {
		return nil, err
	}
	criteria := &imap.SearchCriteria{
		Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: marker}},
	}
	if !since.IsZero() {
		criteria.Since = since.UTC()
	}
	search, err := s.client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search: %w", ctxErr(ctx, err))
	}
	uids := search.AllUIDs()
	found := map[string]time.Time{}
	section := &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: []string{header},
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
			text := string(m.FindBodySection(section))
			if m.Envelope != nil {
				text = m.Envelope.Subject + "\n" + text
			}
			arrived := m.InternalDate.UTC()
			for _, token := range tokenRE.FindAllString(text, -1) {
				token = marker + strings.ToLower(strings.TrimPrefix(token, marker))
				if prev, ok := found[token]; !ok || (!arrived.IsZero() && arrived.Before(prev)) {
					found[token] = arrived
				}
			}
		}
	}
	return found, nil
}

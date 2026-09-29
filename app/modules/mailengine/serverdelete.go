package mailengine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"

	"mailcare/app/models"
)

// ServerDeleteResult is the outcome of DeleteFromServer.
type ServerDeleteResult struct {
	Deleted int // messages deleted from the server (or found already gone) and marked in the index
	Skipped int // candidates left on the server (UIDVALIDITY changed, Message-ID missing or different)
}

// serverDeletableRules are the classification rules whose evidence is
// certain enough for the server retention to delete a message from the
// IMAP server for good: a delivery-status report, a mail daemon sender
// address, "Auto-Submitted: auto-replied" and the fixed wording of a
// non-delivery report. Matches on the subject or the display name alone
// (auto_reply_subject, subject_pattern, daemon_display_name) and
// automatically generated messages (auto_generated) are left on the server:
// a person's mail can look like them.
var serverDeletableRules = []string{ruleDSNReport, ruleDaemonSender, ruleAutoReply, ruleBodyPattern}

// CountTargetMessages returns how many messages of an index MailCare
// handles as its targets: the daemon notices classified with certain
// evidence (serverDeletableRules, the same messages the cleanup applies the
// server retention to), whether or not they are still on the IMAP server.
func CountTargetMessages(db *sql.DB) (int, error) {
	return models.CountClassifiedByRules(db, serverDeletableRules)
}

// ErrOtherDeletedFlags is returned by DeleteFromServer on a server without
// UID EXPUNGE when messages other than the ones to delete carry \Deleted
// (flagged by another client): the plain EXPUNGE such a server offers would
// remove them too, so nothing is expunged this time (the next cleanup tries
// again).
var ErrOtherDeletedFlags = errors.New(`other messages in the folder are flagged \Deleted and the server has no UID EXPUNGE (UIDPLUS); nothing was deleted this time`)

// serverDeleteBatch bounds how many UIDs one FETCH / STORE / UID EXPUNGE
// command names.
const serverDeleteBatch = 200

// DeleteFromServer applies the server retention of a mailbox (design 5.6.1):
// the mails of its folder classified as a daemon notice (failures, delays,
// auto-replies and other daemon mail, whatever the state of their group) by
// one of serverDeletableRules (not the ordinary mail, the mails not
// classified yet or the ones matched on weaker evidence) whose date is
// older than keep are deleted from the IMAP
// server for good (\Deleted, then an expunge; no move to a trash folder). The raw
// files and the index rows stay (the local retention removes them later);
// the rows get server_deleted_at so they are not tried again, and a reindex
// carries it over.
//
// Safety: nothing happens without candidates (no connection is made); the
// folder's UIDVALIDITY must equal the one the candidates were fetched under
// (otherwise the UIDs may name other messages and they are skipped); the
// Message-ID the server reports for each UID must equal the indexed one
// (a message without a Message-ID is skipped); a candidate whose UID is no
// longer on the server counts as deleted. A server with UIDPLUS (or
// IMAP4rev2) expunges exactly the chosen UIDs (UID EXPUNGE). A server
// without it only has the plain EXPUNGE, which removes every message flagged
// \Deleted: it is used only when no other message of the folder carries
// \Deleted, checked before the flags are set and again right after
// (otherwise the flags just set are cleared again and ErrOtherDeletedFlags
// is returned). keep <= 0 or a disabled mailbox does nothing.
func DeleteFromServer(ctx context.Context, mailsRoot string, mb *models.Mailbox, password string, keep time.Duration, progress Progress) (*ServerDeleteResult, error) {
	res := &ServerDeleteResult{}
	if mb == nil || keep <= 0 || !mb.Enabled {
		return res, nil
	}
	folder := mb.Folder
	if folder == "" {
		folder = defaultFolder
	}
	db, err := OpenIndex(ctx, mailsRoot, mb.Address, progress)
	if err != nil {
		return res, err
	}
	defer db.Close()
	candidates, err := models.ListServerDeletionCandidates(db, folder, time.Now().Add(-keep), serverDeletableRules)
	if err != nil {
		return res, fmt.Errorf("list server deletion candidates: %w", err)
	}
	if len(candidates) == 0 {
		return res, nil
	}
	report(progress, fmt.Sprintf("%d daemon notice(s) are past the server retention", len(candidates)))

	s, err := connect(ctx, mb, password)
	if err != nil {
		return res, err
	}
	defer s.close(true)
	sel, err := s.selectFolder(ctx, folder)
	if err != nil {
		return res, err
	}
	caps := s.client.Caps()
	uidExpunge := caps.Has(imap.CapUIDPlus) || caps.Has(imap.CapIMAP4rev2)
	if !uidExpunge {
		report(progress, `the server has no UID EXPUNGE (UIDPLUS); a plain EXPUNGE is used when no other message is flagged \Deleted`)
	}

	byUID := map[imap.UID]models.ServerDeletionCandidate{}
	var uids []imap.UID
	for _, c := range candidates {
		if c.UIDValidity != sel.UIDValidity {
			res.Skipped++
			continue
		}
		byUID[imap.UID(c.UID)] = c
		uids = append(uids, imap.UID(c.UID))
	}
	if res.Skipped > 0 {
		report(progress, fmt.Sprintf("skipped %d message(s) fetched under another UIDVALIDITY (the folder was re-created)", res.Skipped))
	}

	for start := 0; start < len(uids); start += serverDeleteBatch {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		batch := uids[start:min(start+serverDeleteBatch, len(uids))]
		serverIDs, err := s.fetchMessageIDs(ctx, batch)
		if err != nil {
			return res, err
		}
		var toDelete []imap.UID
		var done []int64
		for _, uid := range batch {
			c := byUID[uid]
			serverID, onServer := serverIDs[uid]
			switch {
			case !onServer:
				// Already gone (deleted by another client): nothing to do
				// on the server, but it is no longer there.
				done = append(done, c.ID)
			case normalizeMessageID(c.MessageID) == "" || normalizeMessageID(serverID) != normalizeMessageID(c.MessageID):
				res.Skipped++
			default:
				toDelete = append(toDelete, uid)
				done = append(done, c.ID)
			}
		}
		if len(toDelete) > 0 {
			var err error
			if uidExpunge {
				err = s.expungeUIDs(ctx, toDelete)
			} else {
				err = s.expungeOnly(ctx, toDelete)
			}
			if err != nil {
				// The mails already gone from the server are still recorded.
				deleting := map[int64]bool{}
				for _, uid := range toDelete {
					deleting[byUID[uid].ID] = true
				}
				var gone []int64
				for _, id := range done {
					if !deleting[id] {
						gone = append(gone, id)
					}
				}
				if markErr := models.MarkServerDeleted(db, gone, time.Now()); markErr != nil {
					err = errors.Join(err, markErr)
				}
				res.Deleted += len(gone)
				return res, err
			}
		}
		if err := models.MarkServerDeleted(db, done, time.Now()); err != nil {
			return res, fmt.Errorf("record server deletion: %w", err)
		}
		res.Deleted += len(done)
	}
	if res.Skipped > 0 {
		report(progress, fmt.Sprintf("left %d message(s) on the server (UIDVALIDITY changed, or Message-ID missing or different)", res.Skipped))
	}
	report(progress, fmt.Sprintf("deleted %d message(s) from the IMAP server", res.Deleted))
	return res, nil
}

// deletedFlag is the STORE that sets or clears \Deleted.
func deletedFlag(op imap.StoreFlagsOp) *imap.StoreFlags {
	return &imap.StoreFlags{Op: op, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}
}

// expungeUIDs removes exactly the given messages: \Deleted, then UID
// EXPUNGE (UIDPLUS / IMAP4rev2).
func (s *imapSession) expungeUIDs(ctx context.Context, uids []imap.UID) error {
	set := imap.UIDSetNum(uids...)
	if err := s.client.Store(set, deletedFlag(imap.StoreFlagsAdd), nil).Close(); err != nil {
		return fmt.Errorf("flag messages deleted: %w", ctxErr(ctx, err))
	}
	if err := s.client.UIDExpunge(set).Close(); err != nil {
		return fmt.Errorf("expunge messages: %w", ctxErr(ctx, err))
	}
	return nil
}

// expungeOnly removes the given messages on a server without UID EXPUNGE.
// The plain EXPUNGE removes every message flagged \Deleted, so it runs only
// when the flagged messages are exactly the given ones: checked before the
// flags are set (another client's \Deleted message stops the deletion) and
// again right after (a message flagged meanwhile makes the flags just set be
// cleared again). Both cases return ErrOtherDeletedFlags. A message another
// client flags between the second check and the EXPUNGE is removed as well;
// that client asked for its deletion.
func (s *imapSession) expungeOnly(ctx context.Context, uids []imap.UID) error {
	want := map[imap.UID]bool{}
	for _, uid := range uids {
		want[uid] = true
	}
	onlyOurs := func(flagged []imap.UID) bool {
		for _, uid := range flagged {
			if !want[uid] {
				return false
			}
		}
		return true
	}
	flagged, err := s.searchDeleted(ctx)
	if err != nil {
		return err
	}
	if !onlyOurs(flagged) {
		return ErrOtherDeletedFlags
	}
	set := imap.UIDSetNum(uids...)
	if err := s.client.Store(set, deletedFlag(imap.StoreFlagsAdd), nil).Close(); err != nil {
		return fmt.Errorf("flag messages deleted: %w", ctxErr(ctx, err))
	}
	flagged, err = s.searchDeleted(ctx)
	if err != nil || !onlyOurs(flagged) {
		if clearErr := s.client.Store(set, deletedFlag(imap.StoreFlagsDel), nil).Close(); clearErr != nil {
			err = errors.Join(err, fmt.Errorf("clear the deleted flags again: %w", ctxErr(ctx, clearErr)))
		}
		if err != nil {
			return err
		}
		return ErrOtherDeletedFlags
	}
	if err := s.client.Expunge().Close(); err != nil {
		return fmt.Errorf("expunge messages: %w", ctxErr(ctx, err))
	}
	return nil
}

// searchDeleted returns the UIDs of the selected folder flagged \Deleted.
func (s *imapSession) searchDeleted(ctx context.Context) ([]imap.UID, error) {
	data, err := s.client.UIDSearch(&imap.SearchCriteria{Flag: []imap.Flag{imap.FlagDeleted}}, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search deleted messages: %w", ctxErr(ctx, err))
	}
	return data.AllUIDs(), nil
}

// fetchMessageIDs returns the Message-ID (from the envelope) of every given
// UID that is still in the selected folder.
func (s *imapSession) fetchMessageIDs(ctx context.Context, uids []imap.UID) (map[imap.UID]string, error) {
	msgs, err := s.client.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, Envelope: true}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch message ids: %w", ctxErr(ctx, err))
	}
	out := map[imap.UID]string{}
	for _, m := range msgs {
		if m.UID == 0 {
			continue
		}
		id := ""
		if m.Envelope != nil {
			id = m.Envelope.MessageID
		}
		out[m.UID] = id
	}
	return out, nil
}

// normalizeMessageID trims the angle brackets and white space a Message-ID
// may carry, so that the indexed value and the envelope value compare.
func normalizeMessageID(id string) string {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(id), "<>"))
}

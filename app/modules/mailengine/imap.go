package mailengine

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"mailcare/app/models"
)

// fetchBatchSize bounds how many message bodies one FETCH command asks for.
const fetchBatchSize = 20

// dialTimeout bounds the TCP / TLS connection setup.
const dialTimeout = 30 * time.Second

// imapSession wraps a logged-in client with context cancellation: when ctx is
// done the connection is closed, which makes every pending command fail.
type imapSession struct {
	client *imapclient.Client
	stop   chan struct{}
}

// connect dials, waits for the greeting and logs in. The caller must call
// close on every path.
func connect(ctx context.Context, mb *models.Mailbox, password string) (*imapSession, error) {
	if mb == nil {
		return nil, errors.New("mailengine: mailbox is nil")
	}
	host := strings.TrimSpace(mb.ImapHost)
	if host == "" {
		return nil, errors.New("mailengine: IMAP host is empty")
	}
	port := mb.ImapPort
	if port <= 0 {
		port = defaultIMAPPort
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	opts := &imapclient.Options{
		TLSConfig: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
		Dialer:    &net.Dialer{Timeout: dialTimeout},
	}

	var (
		client *imapclient.Client
		err    error
	)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(mb.ImapSecurity)) {
	case imapSecurityStartTLS:
		client, err = imapclient.DialStartTLS(addr, opts)
	case imapSecurityNone:
		client, err = imapclient.DialInsecure(addr, opts)
	case imapSecuritySSL, "":
		client, err = imapclient.DialTLS(addr, opts)
	default:
		return nil, fmt.Errorf("mailengine: unknown IMAP security mode %q", mb.ImapSecurity)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}
	s := &imapSession{client: client, stop: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-s.stop:
		case <-client.Closed():
		}
	}()
	if err := client.Login(mb.ImapUsername, password).Wait(); err != nil {
		s.close(false)
		return nil, fmt.Errorf("login as %s: %w", mb.ImapUsername, ctxErr(ctx, err))
	}
	return s, nil
}

// selectFolder selects the mailbox folder and returns its UIDVALIDITY.
func (s *imapSession) selectFolder(ctx context.Context, folder string) (*imap.SelectData, error) {
	if folder == "" {
		folder = defaultFolder
	}
	data, err := s.client.Select(folder, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("select folder %q: %w", folder, ctxErr(ctx, err))
	}
	return data, nil
}

// close logs out (when asked) and closes the connection.
func (s *imapSession) close(logout bool) {
	if s == nil || s.client == nil {
		return
	}
	select {
	case <-s.stop:
		return
	default:
		close(s.stop)
	}
	if logout {
		done := make(chan struct{})
		go func() {
			_ = s.client.Logout().Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	_ = s.client.Close()
}

// ctxErr prefers the context error over the transport error when the context
// is done (the transport error is then just "connection closed").
func ctxErr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return err
}

// TestConnection connects to the IMAP server, logs in and selects the folder.
func TestConnection(ctx context.Context, mb *models.Mailbox, password string) error {
	s, err := connect(ctx, mb, password)
	if err != nil {
		return err
	}
	defer s.close(true)
	_, err = s.selectFolder(ctx, mb.Folder)
	return err
}

// FetchMailbox downloads the messages not yet indexed (initial_days back on
// the first run, recent_days afterwards), stores their raw and derived files
// and adds their index rows with classified = 0 (design 5.1). No
// classification or grouping happens here; GroupMailbox does that. The
// caller records the outcome on the mailbox row.
func FetchMailbox(ctx context.Context, mailsRoot string, mb *models.Mailbox, password string, progress Progress) (*FetchResult, error) {
	if mb == nil {
		return nil, errors.New("mailengine: mailbox is nil")
	}
	result := &FetchResult{}

	db, err := OpenIndex(ctx, mailsRoot, mb.Address, progress)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	total, _, err := models.CountMessages(db)
	if err != nil {
		return nil, fmt.Errorf("count messages: %w", err)
	}
	days := mb.RecentDays
	window := "recent"
	if total == 0 {
		days = mb.InitialDays
		window = "initial"
	}
	if days <= 0 {
		if total == 0 {
			days = defaultInitialDays
		} else {
			days = defaultRecentDays
		}
	}
	folder := mb.Folder
	if folder == "" {
		folder = defaultFolder
	}
	dir := MailboxDir(mailsRoot, mb.Address)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	port := mb.ImapPort
	if port <= 0 {
		port = defaultIMAPPort
	}
	report(progress, fmt.Sprintf("connecting to %s", net.JoinHostPort(mb.ImapHost, strconv.Itoa(port))))
	s, err := connect(ctx, mb, password)
	if err != nil {
		return nil, err
	}
	defer s.close(true)

	sel, err := s.selectFolder(ctx, folder)
	if err != nil {
		return nil, err
	}
	result.UIDValidity = sel.UIDValidity
	if mb.LastUIDValidity != 0 && uint32(mb.LastUIDValidity) != sel.UIDValidity {
		report(progress, fmt.Sprintf("uidvalidity changed (%d -> %d); re-scanning the window", mb.LastUIDValidity, sel.UIDValidity))
	}

	since := time.Now().UTC().AddDate(0, 0, -days)
	report(progress, fmt.Sprintf("searching since %s (%s window, %d days)", since.Format("2006-01-02"), window, days))
	search, err := s.client.UIDSearch(&imap.SearchCriteria{Since: since}, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search: %w", ctxErr(ctx, err))
	}
	uids := search.AllUIDs()
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })

	// Drop what the index already holds.
	var pending []imap.UID
	for _, uid := range uids {
		exists, err := models.MessageExists(db, sel.UIDValidity, uint32(uid), folder)
		if err != nil {
			return nil, fmt.Errorf("lookup uid %d: %w", uid, err)
		}
		if !exists {
			pending = append(pending, uid)
		}
	}
	report(progress, fmt.Sprintf("found %d messages in the window, %d new", len(uids), len(pending)))
	if len(pending) == 0 {
		return result, nil
	}

	// Sizes first so oversized messages are never downloaded.
	sizes, dates, err := s.fetchSizes(ctx, pending)
	if err != nil {
		return nil, err
	}
	var toFetch []imap.UID
	for _, uid := range pending {
		if size, ok := sizes[uid]; ok && size > maxMessageSize {
			result.Skipped++
			report(progress, fmt.Sprintf("skipping uid %d (%d bytes exceeds the limit)", uid, size))
			continue
		}
		toFetch = append(toFetch, uid)
	}

	for start := 0; start < len(toFetch); start += fetchBatchSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+fetchBatchSize, len(toFetch))
		batch := toFetch[start:end]
		report(progress, fmt.Sprintf("fetching %d of %d", end, len(toFetch)))
		err := s.fetchBodies(ctx, batch, func(uid imap.UID, internalDate time.Time, raw []byte, tooLarge bool) error {
			if tooLarge {
				result.Skipped++
				report(progress, fmt.Sprintf("skipping uid %d (body exceeds the limit)", uid))
				return nil
			}
			if internalDate.IsZero() {
				internalDate = dates[uid]
			}
			src := Source{
				Folder:      folder,
				UIDValidity: sel.UIDValidity,
				UID:         uint32(uid),
				Size:        sizes[uid],
				ReceivedAt:  internalDate.UTC(),
				FetchedAt:   time.Now().UTC(),
			}
			_, _, err := storeMessage(db, dir, raw, src, storeOptions{writeEML: true, dedupeByMessageID: true})
			if err != nil {
				if errors.Is(err, errDuplicateMessage) || isUniqueViolation(err) {
					// Already indexed (same Message-ID after a UIDVALIDITY
					// change, or the same key): count it as skipped.
					result.Skipped++
					return nil
				}
				return err
			}
			result.Fetched++
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	report(progress, fmt.Sprintf("indexed %d messages, %d skipped", result.Fetched, result.Skipped))
	return result, nil
}

// fetchSizes retrieves RFC822.SIZE and INTERNALDATE of the given UIDs.
func (s *imapSession) fetchSizes(ctx context.Context, uids []imap.UID) (map[imap.UID]int64, map[imap.UID]time.Time, error) {
	sizes := map[imap.UID]int64{}
	dates := map[imap.UID]time.Time{}
	for start := 0; start < len(uids); start += 500 {
		end := min(start+500, len(uids))
		set := imap.UIDSetNum(uids[start:end]...)
		msgs, err := s.client.Fetch(set, &imap.FetchOptions{UID: true, RFC822Size: true, InternalDate: true}).Collect()
		if err != nil {
			return nil, nil, fmt.Errorf("fetch sizes: %w", ctxErr(ctx, err))
		}
		for _, m := range msgs {
			if m.UID == 0 {
				continue
			}
			sizes[m.UID] = m.RFC822Size
			dates[m.UID] = m.InternalDate
		}
	}
	return sizes, dates, nil
}

// fetchBodies downloads the full bodies of the given UIDs (BODY.PEEK[] keeps
// the messages unread) and hands each one to handle in server order. A body
// longer than maxMessageSize is drained and reported with tooLarge = true.
func (s *imapSession) fetchBodies(ctx context.Context, uids []imap.UID,
	handle func(uid imap.UID, internalDate time.Time, raw []byte, tooLarge bool) error) error {
	section := &imap.FetchItemBodySection{Peek: true}
	cmd := s.client.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		UID:          true,
		InternalDate: true,
		RFC822Size:   true,
		BodySection:  []*imap.FetchItemBodySection{section},
	})
	defer cmd.Close()

	var handleErr error
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		if handleErr != nil {
			// Keep draining so that the command completes cleanly.
			continue
		}
		var (
			uid          imap.UID
			internalDate time.Time
			raw          []byte
			gotBody      bool
			tooLarge     bool
		)
		for {
			item := msg.Next()
			if item == nil {
				break
			}
			switch it := item.(type) {
			case imapclient.FetchItemDataUID:
				uid = it.UID
			case imapclient.FetchItemDataInternalDate:
				internalDate = it.Time
			case imapclient.FetchItemDataBodySection:
				if it.Literal == nil {
					continue
				}
				b, err := io.ReadAll(io.LimitReader(it.Literal, maxMessageSize+1))
				if err != nil {
					handleErr = fmt.Errorf("read body: %w", ctxErr(ctx, err))
					_, _ = io.Copy(io.Discard, it.Literal)
					continue
				}
				if len(b) > maxMessageSize {
					tooLarge = true
					_, _ = io.Copy(io.Discard, it.Literal)
					continue
				}
				raw = b
				gotBody = true
			}
		}
		if handleErr != nil {
			continue
		}
		if uid == 0 {
			continue
		}
		if !gotBody && !tooLarge {
			handleErr = fmt.Errorf("uid %d: server returned no body", uid)
			continue
		}
		if err := handle(uid, internalDate, raw, tooLarge); err != nil {
			handleErr = err
		}
	}
	if err := cmd.Close(); err != nil && handleErr == nil {
		return fmt.Errorf("fetch bodies: %w", ctxErr(ctx, err))
	}
	return handleErr
}

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint error.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}

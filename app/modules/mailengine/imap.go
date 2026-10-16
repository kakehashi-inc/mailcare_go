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
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"mailcare/app/models"
)

// fetchBatchSize bounds how many message bodies one FETCH command asks for.
const fetchBatchSize = 20

// Only the connection to an IMAP server is bounded: the TCP connect within
// IMAPConnectTimeout, the TLS handshake of an implicit TLS server within
// IMAPTLSHandshakeTimeout, and the server greeting (and the STARTTLS
// exchange with its handshake) within IMAPConnectTimeout after the TCP
// connect. Once connected nothing has a time limit: no command, no read and
// no write; only the end of ctx closes the connection. The values are the
// owner's (no standard defines them for an IMAP client).
const (
	IMAPConnectTimeout      = 30 * time.Second
	IMAPTLSHandshakeTimeout = 10 * time.Second
)

// imapConnectTimeout and imapTLSHandshakeTimeout are the constants above;
// variables so that the tests can shorten them.
var (
	imapConnectTimeout      = IMAPConnectTimeout
	imapTLSHandshakeTimeout = IMAPTLSHandshakeTimeout
)

// keptDeadlines is the connection the IMAP client works on: it ignores the
// deadlines the client sets itself (go-imap gives each response 30 s and
// each command write 30 s), so the only deadlines are those of the
// connection phase, set on the underlying connection by connect.
type keptDeadlines struct{ net.Conn }

func (keptDeadlines) SetDeadline(time.Time) error      { return nil }
func (keptDeadlines) SetReadDeadline(time.Time) error  { return nil }
func (keptDeadlines) SetWriteDeadline(time.Time) error { return nil }

// imapSession wraps a logged-in client with context cancellation: when ctx is
// done the connection is closed, which makes every pending command fail.
type imapSession struct {
	client     *imapclient.Client
	stopCancel func() bool // ends the closing of the connection at the end of ctx
	closeOnce  sync.Once
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
	}
	security := strings.ToLower(strings.TrimSpace(mb.ImapSecurity))
	switch security {
	case imapSecurityStartTLS, imapSecurityNone, imapSecuritySSL, "":
	default:
		return nil, fmt.Errorf("mailengine: unknown IMAP security mode %q", mb.ImapSecurity)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: imapConnectTimeout}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, ctxErr(ctx, err))
	}
	// A cancellation closes the connection at once, also during the TLS
	// handshake, the greeting and STARTTLS.
	stopCancel := context.AfterFunc(ctx, func() { _ = raw.Close() })
	fail := func(err error) (*imapSession, error) {
		stopCancel()
		_ = raw.Close()
		return nil, fmt.Errorf("connect to %s: %w", addr, ctxErr(ctx, err))
	}
	// The connection phase: the handshake, the greeting and STARTTLS.
	if err := raw.SetDeadline(time.Now().Add(imapConnectTimeout)); err != nil {
		return fail(err)
	}
	conn := raw
	if security == imapSecuritySSL || security == "" {
		tlsConn := tls.Client(raw, opts.TLSConfig)
		hctx, cancel := context.WithTimeout(ctx, imapTLSHandshakeTimeout)
		err := tlsConn.HandshakeContext(hctx)
		cancel()
		if err != nil {
			return fail(err)
		}
		conn = tlsConn
	}
	var client *imapclient.Client
	if security == imapSecurityStartTLS {
		if client, err = imapclient.NewStartTLS(keptDeadlines{conn}, opts); err != nil {
			return fail(err)
		}
	} else {
		client = imapclient.New(keptDeadlines{conn}, opts)
		if err := client.WaitGreeting(); err != nil {
			_ = client.Close()
			return fail(err)
		}
	}
	// Connected: no deadline from here on.
	if err := raw.SetDeadline(time.Time{}); err != nil {
		_ = client.Close()
		return fail(err)
	}
	s := &imapSession{client: client, stopCancel: stopCancel}
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

// close sends LOGOUT (when asked) and closes the connection. The reply to
// LOGOUT is not awaited: nothing depends on it (RFC 9051 6.1.3, the server
// closes the connection after its BYE anyway).
func (s *imapSession) close(logout bool) {
	if s == nil || s.client == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.stopCancel()
		if logout {
			_ = s.client.Logout()
		}
		_ = s.client.Close()
	})
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
// the first run, recent_days afterwards, but never before opts.NotBefore
// when it is set; the whole folder with opts.AllTime), stores their raw and
// derived files and adds their index rows with classified = 0 (design 5.1).
// Every UID of the folder is a separate message: messages that share a
// Message-ID (the same mail delivered more than once) are each indexed. A
// message already indexed under a stale identity (the folder's UIDVALIDITY
// changed, or it was indexed from a raw file) is not stored again; its row
// takes the new UID (storeOptions.reidentify). No classification or
// grouping happens here; GroupMailbox does that. The caller records the
// outcome on the mailbox row.
func FetchMailbox(ctx context.Context, mailsRoot string, mb *models.Mailbox, password string, opts FetchOptions, progress Progress) (*FetchResult, error) {
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
	if total > 0 {
		// The index holds messages but none under this UIDVALIDITY: the
		// folder was re-created and the window is re-scanned (the indexed
		// messages are recognised by Message-ID and take their new UID).
		maxUID, err := models.MaxUIDForValidity(db, sel.UIDValidity, folder)
		if err != nil {
			return nil, fmt.Errorf("lookup uidvalidity: %w", err)
		}
		if maxUID == 0 {
			report(progress, fmt.Sprintf("uidvalidity changed (now %d); re-scanning the window", sel.UIDValidity))
		}
	}

	criteria := &imap.SearchCriteria{}
	if opts.AllTime {
		report(progress, "searching the whole folder (all time)")
	} else {
		since := time.Now().UTC().AddDate(0, 0, -days)
		bounded := ""
		if !opts.NotBefore.IsZero() && opts.NotBefore.After(since) {
			since = opts.NotBefore.UTC()
			bounded = ", limited to the mail retention"
		}
		report(progress, fmt.Sprintf("searching since %s (%s window, %d days%s)", since.Format("2006-01-02"), window, days, bounded))
		criteria.Since = since
	}
	search, err := s.client.UIDSearch(criteria, nil).Wait()
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
	report(progress, fmt.Sprintf("found %d messages, %d new", len(uids), len(pending)))
	if len(pending) == 0 {
		return result, nil
	}

	// Sizes and dates first (recorded on the row; no message is refused for
	// its size).
	sizes, dates, err := s.fetchSizes(ctx, pending)
	if err != nil {
		return nil, err
	}
	toFetch := pending

	for start := 0; start < len(toFetch); start += fetchBatchSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+fetchBatchSize, len(toFetch))
		batch := toFetch[start:end]
		report(progress, fmt.Sprintf("fetching %d of %d", end, len(toFetch)))
		err := s.fetchBodies(ctx, batch, func(uid imap.UID, internalDate time.Time, raw []byte) error {
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
			_, _, err := storeMessage(db, dir, raw, src, storeOptions{writeEML: true, reidentify: true})
			if err != nil {
				if errors.Is(err, errReidentified) {
					result.Reidentified++
					return nil
				}
				if isUniqueViolation(err) {
					// The same identity was indexed meanwhile (a concurrent
					// fetch of this mailbox): nothing is missing.
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

	line := fmt.Sprintf("indexed %d messages", result.Fetched)
	if result.Reidentified > 0 {
		line += fmt.Sprintf(", %d already indexed under a previous UID", result.Reidentified)
	}
	report(progress, line)
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
// the messages unread) and hands each one to handle in server order, whole
// and unmodified whatever its size.
func (s *imapSession) fetchBodies(ctx context.Context, uids []imap.UID,
	handle func(uid imap.UID, internalDate time.Time, raw []byte) error) error {
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
				b, err := io.ReadAll(it.Literal)
				if err != nil {
					handleErr = fmt.Errorf("read body: %w", ctxErr(ctx, err))
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
		if !gotBody {
			handleErr = fmt.Errorf("uid %d: server returned no body", uid)
			continue
		}
		if err := handle(uid, internalDate, raw); err != nil {
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

package receiver

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const (
	imapInitialBackoff = 30 * time.Minute
	imapMaxBackoff     = 60 * time.Minute
)

// IMAPReceiver fetches emails over IMAP/IMAPS and implements Watcher via IDLE.
type IMAPReceiver struct {
	name         string
	host         string
	port         int
	username     string
	password     string
	useTLS       bool
	folder       string
	pollInterval time.Duration // fallback interval when IDLE is unsupported
	logger       *slog.Logger
}

// NewIMAP creates a new IMAP receiver.
func NewIMAP(name, host string, port int, username, password string, useTLS bool, folder string, pollInterval time.Duration, logger *slog.Logger) *IMAPReceiver {
	if folder == "" {
		folder = "INBOX"
	}
	return &IMAPReceiver{
		name:         name,
		host:         host,
		port:         port,
		username:     username,
		password:     password,
		useTLS:       useTLS,
		folder:       folder,
		pollInterval: pollInterval,
		logger:       logger,
	}
}

// Fetch opens a one-shot connection, retrieves new emails, and closes.
func (r *IMAPReceiver) Fetch(seenIDs map[string]struct{}, processDays int) ([]Email, error) {
	client, err := r.dial(nil)
	if err != nil {
		return nil, err
	}
	defer r.logout(client)

	if _, err := client.Select(r.folder, nil).Wait(); err != nil {
		return nil, fmt.Errorf("imap select %s: %w", r.folder, err)
	}

	return r.fetchMessages(client, seenIDs, processDays)
}

// Watch maintains a persistent connection, using IMAP IDLE when the server
// supports it (advertised in capabilities) and falling back to timed polling
// on the same connection otherwise. Reconnects with exponential backoff.
func (r *IMAPReceiver) Watch(ctx context.Context, getSeenIDs func() map[string]struct{}, processDays int, onNew func([]Email)) {
	backoff := imapInitialBackoff
	for {
		if err := r.runSession(ctx, getSeenIDs, processDays, onNew); ctx.Err() != nil {
			return
		} else {
			r.logger.Error("imap session ended, reconnecting",
				"account", r.name,
				"error", err,
				"backoff", backoff,
			)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, imapMaxBackoff)
	}
}

// runSession connects, selects the folder, performs an initial fetch, then
// dispatches to idleLoop or pollLoop based on server capabilities.
func (r *IMAPReceiver) runSession(ctx context.Context, getSeenIDs func() map[string]struct{}, processDays int, onNew func([]Email)) error {
	notify := make(chan struct{}, 1)

	client, err := r.dial(&imapclient.UnilateralDataHandler{
		Mailbox: func(data *imapclient.UnilateralDataMailbox) {
			if data.NumMessages != nil {
				select {
				case notify <- struct{}{}:
				default:
				}
			}
		},
	})
	if err != nil {
		return err
	}
	defer r.logout(client)

	caps := client.Caps()
	if caps.Has(imap.CapIdle) {
		r.logger.Info("using IMAP IDLE", "account", r.name)
	} else {
		r.logger.Info("using polling", "account", r.name, "interval", r.pollInterval)
	}

	if _, err := client.Select(r.folder, nil).Wait(); err != nil {
		return fmt.Errorf("imap select %s: %w", r.folder, err)
	}

	// Initial fetch on connect.
	r.deliverNew(client, getSeenIDs(), processDays, onNew)

	if caps.Has(imap.CapIdle) {
		return r.idleLoop(ctx, client, notify, getSeenIDs, processDays, onNew)
	}
	r.pollLoop(ctx, getSeenIDs, processDays, onNew)
	return nil
}

// idleLoop blocks in IDLE, waking on server notifications to fetch new mail.
func (r *IMAPReceiver) idleLoop(ctx context.Context, client *imapclient.Client, notify <-chan struct{}, getSeenIDs func() map[string]struct{}, processDays int, onNew func([]Email)) error {
	idleCmd, err := client.Idle()
	if err != nil {
		return fmt.Errorf("imap idle: %w", err)
	}

	for {
		idleDone := make(chan error, 1)
		go func() { idleDone <- idleCmd.Wait() }()

		select {
		case <-ctx.Done():
			idleCmd.Close()
			<-idleDone
			return nil

		case err := <-idleDone:
			return fmt.Errorf("imap idle ended unexpectedly: %w", err)

		case <-notify:
			if err := idleCmd.Close(); err != nil {
				return fmt.Errorf("imap idle close: %w", err)
			}
			if err := <-idleDone; err != nil {
				return fmt.Errorf("imap idle wait: %w", err)
			}
			r.deliverNew(client, getSeenIDs(), processDays, onNew)

			if idleCmd, err = client.Idle(); err != nil {
				return fmt.Errorf("imap idle restart: %w", err)
			}
		}
	}
}

// pollLoop polls on r.pollInterval, opening a fresh connection for each tick.
// A fresh connection per tick avoids server-side idle-timeout errors that occur
// when a persistent connection sits unused for the full poll interval.
func (r *IMAPReceiver) pollLoop(ctx context.Context, getSeenIDs func() map[string]struct{}, processDays int, onNew func([]Email)) {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			emails, err := r.Fetch(getSeenIDs(), processDays)
			if err != nil {
				r.logger.Error("imap fetch failed", "account", r.name, "error", err)
				continue
			}
			if len(emails) > 0 {
				onNew(emails)
			}
		}
	}
}

func (r *IMAPReceiver) deliverNew(client *imapclient.Client, seenIDs map[string]struct{}, processDays int, onNew func([]Email)) {
	emails, err := r.fetchMessages(client, seenIDs, processDays)
	if err != nil {
		r.logger.Error("imap fetch failed", "account", r.name, "error", err)
		return
	}
	if len(emails) > 0 {
		onNew(emails)
	}
}

// fetchMessages searches for and retrieves new emails on an already-selected client.
// It uses UID-based search and fetch for stable message identification.
func (r *IMAPReceiver) fetchMessages(client *imapclient.Client, seenIDs map[string]struct{}, processDays int) ([]Email, error) {
	since := time.Now().AddDate(0, 0, -processDays)
	searchData, err := client.UIDSearch(&imap.SearchCriteria{Since: since}, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap search: %w", err)
	}

	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		r.logger.Debug("no messages found in date range", "account", r.name)
		return nil, nil
	}
	r.logger.Info("found messages in date range", "account", r.name, "count", len(uids))

	fetchOpts := &imap.FetchOptions{
		UID:         true,
		Envelope:    true,
		BodySection: []*imap.FetchItemBodySection{{Peek: true}},
	}
	msgs, err := client.Fetch(imap.UIDSetNum(uids...), fetchOpts).Collect()
	if err != nil {
		return nil, fmt.Errorf("imap fetch: %w", err)
	}

	bodySection := &imap.FetchItemBodySection{Peek: true}
	var emails []Email
	for _, msg := range msgs {
		msgID := ""
		if msg.Envelope != nil {
			msgID = msg.Envelope.MessageID
		}
		if msgID == "" {
			msgID = fmt.Sprintf("imap-uid-%d-%s", msg.UID, r.username)
		}
		if _, seen := seenIDs[msgID]; seen {
			continue
		}

		content := msg.FindBodySection(bodySection)
		if len(content) == 0 {
			r.logger.Warn("empty body, skipping", "account", r.name, "msg_id", msgID)
			continue
		}

		var date time.Time
		if msg.Envelope != nil {
			date = msg.Envelope.Date
		}
		emails = append(emails, Email{ID: msgID, Date: date, Content: content})
	}

	r.logger.Info("filtered emails", "account", r.name, "new", len(emails))
	return emails, nil
}

// dial creates an authenticated IMAP connection.
// handler may be nil for one-shot (non-Watch) connections.
func (r *IMAPReceiver) dial(handler *imapclient.UnilateralDataHandler) (*imapclient.Client, error) {
	addr := net.JoinHostPort(r.host, fmt.Sprintf("%d", r.port))
	opts := &imapclient.Options{
		TLSConfig:             &tls.Config{ServerName: r.host},
		UnilateralDataHandler: handler,
	}

	var (
		client *imapclient.Client
		err    error
	)
	if r.useTLS {
		client, err = imapclient.DialTLS(addr, opts)
	} else {
		client, err = imapclient.DialInsecure(addr, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("imap connect %s: %w", addr, err)
	}
	if err := client.Login(r.username, r.password).Wait(); err != nil {
		client.Close()
		return nil, fmt.Errorf("imap login %s: %w", r.username, err)
	}
	return client, nil
}

// logout cleanly signs off and closes the connection.
func (r *IMAPReceiver) logout(client *imapclient.Client) {
	if err := client.Logout().Wait(); err != nil {
		r.logger.Debug("imap logout", "account", r.name, "error", err)
	}
	client.Close()
}

func (r *IMAPReceiver) Close() error {
	return nil
}

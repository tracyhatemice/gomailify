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
	imapInitialBackoff = 5 * time.Second
	imapMaxBackoff     = 10 * time.Minute
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
	client, err := r.connect()
	if err != nil {
		return nil, err
	}
	defer client.Logout()
	defer client.Close()

	if _, err := client.Select(r.folder, nil).Wait(); err != nil {
		return nil, fmt.Errorf("imap select %s: %w", r.folder, err)
	}

	return r.fetchMessages(client, seenIDs, processDays)
}

// Watch maintains a persistent connection, using IMAP IDLE when the server
// supports it (advertised in capabilities) and falling back to timed polling
// on the same connection otherwise. Reconnects with exponential backoff.
func (r *IMAPReceiver) Watch(ctx context.Context, getSeenIDs func() map[string]struct{}, processDays int, onNew func([]Email)) {
	backoffDur := imapInitialBackoff
	for {
		r.logger.Debug("imap connecting", "account", r.name)
		err := r.runSession(ctx, getSeenIDs, processDays, onNew)
		if ctx.Err() != nil {
			return
		}
		r.logger.Error("imap session ended, reconnecting",
			"account", r.name,
			"error", err,
			"backoff", backoffDur,
		)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoffDur):
		}
		backoffDur = min(backoffDur*2, imapMaxBackoff)
	}
}

// runSession connects, does an initial fetch, then dispatches to the IDLE loop
// or the polling loop depending on server capabilities.
func (r *IMAPReceiver) runSession(ctx context.Context, getSeenIDs func() map[string]struct{}, processDays int, onNew func([]Email)) error {
	notify := make(chan struct{}, 1)

	opts := r.clientOptions(&imapclient.UnilateralDataHandler{
		Mailbox: func(data *imapclient.UnilateralDataMailbox) {
			if data.NumMessages != nil {
				select {
				case notify <- struct{}{}:
				default:
				}
			}
		},
	})

	client, err := r.connectWithOptions(opts)
	if err != nil {
		return err
	}
	defer client.Logout()
	defer client.Close()

	if _, err := client.Select(r.folder, nil).Wait(); err != nil {
		return fmt.Errorf("imap select %s: %w", r.folder, err)
	}

	// Initial fetch on connect.
	r.deliverNew(client, getSeenIDs(), processDays, onNew)

	if client.Caps().Has(imap.CapIdle) {
		r.logger.Info("using IMAP IDLE", "account", r.name)
		return r.runIDLELoop(ctx, client, notify, getSeenIDs, processDays, onNew)
	}

	r.logger.Info("using polling", "account", r.name, "interval", r.pollInterval)
	return r.runPollingLoop(ctx, client, getSeenIDs, processDays, onNew)
}

// runIDLELoop blocks in IDLE, waking on server notifications to fetch new mail.
func (r *IMAPReceiver) runIDLELoop(ctx context.Context, client *imapclient.Client, notify <-chan struct{}, getSeenIDs func() map[string]struct{}, processDays int, onNew func([]Email)) error {
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
			return fmt.Errorf("imap idle ended: %w", err)

		case <-notify:
			if err := idleCmd.Close(); err != nil {
				return fmt.Errorf("imap idle close: %w", err)
			}
			if err := <-idleDone; err != nil {
				return fmt.Errorf("imap idle wait: %w", err)
			}
			r.deliverNew(client, getSeenIDs(), processDays, onNew)

			idleCmd, err = client.Idle()
			if err != nil {
				return fmt.Errorf("imap idle restart: %w", err)
			}
		}
	}
}

// runPollingLoop polls on r.pollInterval using the existing connection.
func (r *IMAPReceiver) runPollingLoop(ctx context.Context, client *imapclient.Client, getSeenIDs func() map[string]struct{}, processDays int, onNew func([]Email)) error {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.deliverNew(client, getSeenIDs(), processDays, onNew)
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
func (r *IMAPReceiver) fetchMessages(client *imapclient.Client, seenIDs map[string]struct{}, processDays int) ([]Email, error) {
	since := time.Now().AddDate(0, 0, -processDays)
	searchData, err := client.Search(&imap.SearchCriteria{Since: since}, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap search: %w", err)
	}

	seqNums := searchData.AllSeqNums()
	if len(seqNums) == 0 {
		r.logger.Debug("no messages found in date range", "account", r.name)
		return nil, nil
	}
	r.logger.Info("found messages in date range", "account", r.name, "count", len(seqNums))

	fetchOpts := &imap.FetchOptions{
		Envelope:    true,
		BodySection: []*imap.FetchItemBodySection{{Peek: true}},
	}
	buffers, err := client.Fetch(imap.SeqSetNum(seqNums...), fetchOpts).Collect()
	if err != nil {
		return nil, fmt.Errorf("imap fetch: %w", err)
	}

	bodySection := &imap.FetchItemBodySection{Peek: true}
	var emails []Email
	for _, buf := range buffers {
		var msgID string
		if buf.Envelope != nil {
			msgID = buf.Envelope.MessageID
		}
		if msgID == "" {
			msgID = fmt.Sprintf("imap-%d-%s", buf.SeqNum, r.username)
		}
		if _, seen := seenIDs[msgID]; seen {
			continue
		}

		content := buf.FindBodySection(bodySection)
		if len(content) == 0 {
			r.logger.Warn("empty body, skipping", "account", r.name, "msg_id", msgID)
			continue
		}

		var date time.Time
		if buf.Envelope != nil {
			date = buf.Envelope.Date
		}
		emails = append(emails, Email{ID: msgID, Date: date, Content: content})
	}

	r.logger.Info("filtered emails", "account", r.name, "new", len(emails))
	return emails, nil
}

func (r *IMAPReceiver) connect() (*imapclient.Client, error) {
	return r.connectWithOptions(r.clientOptions(nil))
}

func (r *IMAPReceiver) connectWithOptions(opts *imapclient.Options) (*imapclient.Client, error) {
	addr := net.JoinHostPort(r.host, fmt.Sprintf("%d", r.port))
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

func (r *IMAPReceiver) clientOptions(handler *imapclient.UnilateralDataHandler) *imapclient.Options {
	return &imapclient.Options{
		TLSConfig:             &tls.Config{ServerName: r.host},
		UnilateralDataHandler: handler,
	}
}

func (r *IMAPReceiver) Close() error {
	return nil
}

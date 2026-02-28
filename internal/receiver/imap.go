package receiver

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// IMAPReceiver fetches emails over IMAP/IMAPS.
type IMAPReceiver struct {
	host     string
	port     int
	username string
	password string
	useTLS   bool
	folder   string
	logger   *slog.Logger
}

// NewIMAP creates a new IMAP receiver.
func NewIMAP(host string, port int, username, password string, useTLS bool, folder string, logger *slog.Logger) *IMAPReceiver {
	if folder == "" {
		folder = "INBOX"
	}
	return &IMAPReceiver{
		host:     host,
		port:     port,
		username: username,
		password: password,
		useTLS:   useTLS,
		folder:   folder,
		logger:   logger,
	}
}

func (r *IMAPReceiver) Fetch(seenIDs map[string]struct{}, processDays int) ([]Email, error) {
	addr := net.JoinHostPort(r.host, fmt.Sprintf("%d", r.port))

	var client *imapclient.Client
	var err error

	if r.useTLS {
		client, err = imapclient.DialTLS(addr, &imapclient.Options{
			TLSConfig: &tls.Config{ServerName: r.host},
		})
	} else {
		client, err = imapclient.DialInsecure(addr, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("imap connect %s: %w", addr, err)
	}
	defer client.Close()

	if err := client.Login(r.username, r.password).Wait(); err != nil {
		return nil, fmt.Errorf("imap login %s: %w", r.username, err)
	}
	defer client.Logout()

	if _, err := client.Select(r.folder, nil).Wait(); err != nil {
		return nil, fmt.Errorf("imap select %s: %w", r.folder, err)
	}

	since := time.Now().AddDate(0, 0, -processDays)
	searchCriteria := &imap.SearchCriteria{
		Since: since,
	}
	searchData, err := client.Search(searchCriteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap search: %w", err)
	}

	seqNums := searchData.AllSeqNums()
	if len(seqNums) == 0 {
		r.logger.Info("no messages found in date range")
		return nil, nil
	}

	r.logger.Info("found messages in date range", "count", len(seqNums))

	seqSet := imap.SeqSetNum(seqNums...)
	fetchOptions := &imap.FetchOptions{
		Envelope: true,
		BodySection: []*imap.FetchItemBodySection{
			{Peek: true},
		},
	}

	fetchCmd := client.Fetch(seqSet, fetchOptions)

	// Collect all messages into buffered form for easier access.
	buffers, err := fetchCmd.Collect()
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
			r.logger.Warn("empty body, skipping", "msg_id", msgID)
			continue
		}

		var emailDate time.Time
		if buf.Envelope != nil {
			emailDate = buf.Envelope.Date
		}

		emails = append(emails, Email{
			ID:      msgID,
			Date:    emailDate,
			Content: content,
		})
	}

	r.logger.Info("filtered emails", "new", len(emails))
	return emails, nil
}

func (r *IMAPReceiver) Close() error {
	return nil
}

package receiver

import (
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/emersion/go-message/mail"
	pop3client "github.com/knadh/go-pop3"
)

// POP3Receiver fetches emails over POP3/POP3S.
type POP3Receiver struct {
	host     string
	port     int
	username string
	password string
	useTLS   bool
	logger   *slog.Logger
}

// NewPOP3 creates a new POP3 receiver.
func NewPOP3(host string, port int, username, password string, useTLS bool, logger *slog.Logger) *POP3Receiver {
	return &POP3Receiver{
		host:     host,
		port:     port,
		username: username,
		password: password,
		useTLS:   useTLS,
		logger:   logger,
	}
}

func (r *POP3Receiver) Fetch(seenIDs map[string]struct{}, processDays int) ([]Email, error) {
	addr := net.JoinHostPort(r.host, fmt.Sprintf("%d", r.port))

	opt := pop3client.Opt{
		Host:       r.host,
		Port:       r.port,
		TLSEnabled: r.useTLS,
	}

	client := pop3client.New(opt)
	conn, err := client.NewConn()
	if err != nil {
		return nil, fmt.Errorf("pop3 connect %s: %w", addr, err)
	}
	defer conn.Quit()

	if err := conn.Auth(r.username, r.password); err != nil {
		return nil, fmt.Errorf("pop3 auth %s: %w", r.username, err)
	}

	msgs, err := conn.List(0)
	if err != nil {
		return nil, fmt.Errorf("pop3 list: %w", err)
	}

	r.logger.Info("fetched message list", "count", len(msgs))

	cutoff := time.Now().AddDate(0, 0, -processDays)
	var emails []Email

	for _, msg := range msgs {
		rawBuf, err := conn.RetrRaw(msg.ID)
		if err != nil {
			r.logger.Warn("pop3 retrieve failed", "msg_id", msg.ID, "error", err)
			continue
		}
		raw := rawBuf.Bytes()

		msgID := extractMessageID(raw)
		if msgID == "" {
			// Fall back to UIDL if available, otherwise use sequence + username.
			if msg.UID != "" {
				msgID = fmt.Sprintf("pop3-uid-%s-%s", msg.UID, r.username)
			} else {
				msgID = fmt.Sprintf("pop3-%d-%s", msg.ID, r.username)
			}
		}

		if _, seen := seenIDs[msgID]; seen {
			continue
		}

		date := extractDate(raw)
		if !date.IsZero() && date.Before(cutoff) {
			continue
		}

		emails = append(emails, Email{
			ID:      msgID,
			Date:    date,
			Content: raw,
		})
	}

	r.logger.Info("filtered emails", "new", len(emails))
	return emails, nil
}

func (r *POP3Receiver) Close() error {
	return nil
}

// extractMessageID parses Message-ID from raw email bytes.
func extractMessageID(raw []byte) string {
	reader, err := mail.CreateReader(strings.NewReader(string(raw)))
	if err != nil {
		return ""
	}
	defer reader.Close()
	return reader.Header.Get("Message-ID")
}

// extractDate parses the Date header from raw email bytes.
func extractDate(raw []byte) time.Time {
	reader, err := mail.CreateReader(strings.NewReader(string(raw)))
	if err != nil {
		return time.Time{}
	}
	defer reader.Close()
	date, err := reader.Header.Date()
	if err != nil {
		return time.Time{}
	}
	return date
}

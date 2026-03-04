package receiver

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/emersion/go-message/mail"
	pop3client "github.com/knadh/go-pop3"
)

// POP3Receiver fetches emails over POP3/POP3S.
type POP3Receiver struct {
	name     string
	host     string
	port     int
	username string
	password string
	useTLS   bool
	logger   *slog.Logger

	knownUIDs     map[string]struct{} // cached server UIDs from last poll
	uidlSupported *bool               // nil=untested, then true/false
}

// NewPOP3 creates a new POP3 receiver.
func NewPOP3(name, host string, port int, username, password string, useTLS bool, logger *slog.Logger) *POP3Receiver {
	return &POP3Receiver{
		name:      name,
		host:      host,
		port:      port,
		username:  username,
		password:  password,
		useTLS:    useTLS,
		logger:    logger,
		knownUIDs: make(map[string]struct{}),
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

	r.logger.Info("fetched message list", "account", r.name, "count", len(msgs))

	// Try UIDL to detect new messages without downloading.
	uidMap := r.fetchUIDs(conn)

	cutoff := time.Now().AddDate(0, 0, -processDays)
	var emails []Email
	var skipped int

	for _, msg := range msgs {
		uid := uidMap[msg.ID]

		// If UIDL available and UID already known, skip download.
		if uid != "" {
			if _, known := r.knownUIDs[uid]; known {
				skipped++
				continue
			}
		}

		rawBuf, err := conn.RetrRaw(msg.ID)
		if err != nil {
			r.logger.Warn("pop3 retrieve failed", "msg_id", msg.ID, "error", err)
			continue
		}
		raw := rawBuf.Bytes()

		msgID := extractMessageID(raw)
		if msgID == "" {
			if uid != "" {
				msgID = fmt.Sprintf("pop3-uid-%s-%s", uid, r.username)
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

	// Update knownUIDs to current server snapshot.
	if len(uidMap) > 0 {
		newKnown := make(map[string]struct{}, len(uidMap))
		for _, uid := range uidMap {
			newKnown[uid] = struct{}{}
		}
		r.knownUIDs = newKnown
	}

	r.logger.Info("filtered emails", "account", r.name,
		"new", len(emails), "uidl_skipped", skipped)
	return emails, nil
}

func (r *POP3Receiver) Close() error {
	return nil
}

// fetchUIDs attempts UIDL and returns a map of sequence ID → UID.
// Returns an empty map if UIDL is unsupported.
func (r *POP3Receiver) fetchUIDs(conn *pop3client.Conn) map[int]string {
	if r.uidlSupported != nil && !*r.uidlSupported {
		return nil
	}

	uidls, err := conn.Uidl(0)
	if err != nil {
		supported := false
		r.uidlSupported = &supported
		r.logger.Info("UIDL not supported, falling back to full download", "account", r.name)
		return nil
	}

	supported := true
	r.uidlSupported = &supported

	m := make(map[int]string, len(uidls))
	for _, u := range uidls {
		m[u.ID] = u.UID
	}
	return m
}

// extractMessageID extracts the Message-ID header from raw email bytes.
func extractMessageID(raw []byte) string {
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	return mr.Header.Get("Message-ID")
}

// extractDate parses the Date header from raw email bytes.
func extractDate(raw []byte) time.Time {
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return time.Time{}
	}
	date, err := mr.Header.Date()
	if err != nil {
		return time.Time{}
	}
	return date
}

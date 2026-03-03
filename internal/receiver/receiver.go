package receiver

import (
	"context"
	"time"
)

// Email represents a fetched email message.
type Email struct {
	ID      string    // unique identifier (Message-ID or UID)
	Date    time.Time // date the email was sent/received
	Content []byte    // raw RFC 5322 message bytes
}

// Receiver fetches emails from a remote mail server.
type Receiver interface {
	// Fetch returns emails from approximately the last processDays days.
	// It should skip IDs present in seenIDs.
	Fetch(seenIDs map[string]struct{}, processDays int) ([]Email, error)

	// Close releases any resources held by the receiver.
	Close() error
}

// Watcher is an optional interface for receivers that support server-push
// notifications (e.g. IMAP IDLE). The forwarder uses Watch when available,
// avoiding repeated logins.
type Watcher interface {
	// Watch maintains a persistent connection and calls onNew whenever new
	// emails are available. It reconnects automatically on transient errors
	// and returns only when ctx is cancelled.
	Watch(ctx context.Context, getSeenIDs func() map[string]struct{}, processDays int, onNew func([]Email))
}

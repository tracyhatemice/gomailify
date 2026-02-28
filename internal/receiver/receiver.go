package receiver

import "time"

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

package forwarder

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/tracyhatemice/gomailify/internal/config"
	"github.com/tracyhatemice/gomailify/internal/dedup"
	"github.com/tracyhatemice/gomailify/internal/receiver"
	"github.com/tracyhatemice/gomailify/internal/sender"
)

const maxBackoffMultiplier = 16

// Forwarder monitors one email account and forwards new messages.
type Forwarder struct {
	account  config.Account
	receiver receiver.Receiver
	sender   *sender.Sender
	tracker  *dedup.Tracker
	logger   *slog.Logger
}

// New creates a Forwarder for the given account.
func New(
	acct config.Account,
	recv receiver.Receiver,
	smtp *sender.Sender,
	tracker *dedup.Tracker,
	logger *slog.Logger,
) *Forwarder {
	return &Forwarder{
		account:  acct,
		receiver: recv,
		sender:   smtp,
		tracker:  tracker,
		logger:   logger,
	}
}

// Run starts the forwarder. If the receiver supports IMAP IDLE (Watcher), it
// uses push-based delivery. Otherwise it falls back to interval polling with
// exponential backoff on consecutive errors.
func (f *Forwarder) Run(ctx context.Context) {
	f.logger.Info("starting forwarder",
		"account", f.account.Name,
		"protocol", f.account.Protocol,
		"host", f.account.Host,
	)

	if w, ok := f.receiver.(receiver.Watcher); ok {
		f.logger.Info("using IMAP IDLE", "account", f.account.Name)
		w.Watch(ctx, f.tracker.SeenIDs, f.account.GetProcessDays(), func(emails []receiver.Email) {
			f.forwardEmails(emails)
		})
	} else {
		f.runPoller(ctx)
	}

	f.logger.Info("forwarder stopped", "account", f.account.Name)
}

// runPoller polls on the configured interval with exponential backoff on errors.
func (f *Forwarder) runPoller(ctx context.Context) {
	base := f.account.CheckInterval()
	f.logger.Info("using polling", "account", f.account.Name, "interval", base)

	errCount := 0
	if !f.poll() {
		errCount++
	}

	for {
		wait := backoff(base, errCount)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
			if f.poll() {
				errCount = 0
			} else {
				errCount++
				f.logger.Warn("backing off",
					"account", f.account.Name,
					"consecutive_errors", errCount,
					"next_retry", backoff(base, errCount),
				)
			}
		}
	}
}

// poll fetches and forwards new emails. Returns true on success, false on fetch error.
func (f *Forwarder) poll() bool {
	f.logger.Debug("polling", "account", f.account.Name)
	emails, err := f.receiver.Fetch(f.tracker.SeenIDs(), f.account.GetProcessDays())
	if err != nil {
		f.logger.Error("fetch failed", "account", f.account.Name, "error", err)
		return false
	}
	if len(emails) > 0 {
		f.forwardEmails(emails)
	} else {
		f.logger.Debug("no new emails", "account", f.account.Name)
	}
	return true
}

func (f *Forwarder) forwardEmails(emails []receiver.Email) {
	f.logger.Info(fmt.Sprintf("found %d new email(s)", len(emails)), "account", f.account.Name)
	for _, email := range emails {
		if err := f.sender.Forward(email.Content, f.account.ForwardTo, email.ID); err != nil {
			f.logger.Error("forward failed",
				"account", f.account.Name,
				"msg_id", email.ID,
				"error", err,
			)
			continue
		}
		if err := f.tracker.MarkSeen(email.ID); err != nil {
			f.logger.Error("mark seen failed",
				"account", f.account.Name,
				"msg_id", email.ID,
				"error", err,
			)
			continue
		}
		f.logger.Info("forwarded",
			"account", f.account.Name,
			"msg_id", email.ID,
			"to", f.account.ForwardTo,
		)
	}
}

// backoff returns base * 2^errCount, capped at base * maxBackoffMultiplier.
func backoff(base time.Duration, errCount int) time.Duration {
	if errCount <= 0 {
		return base
	}
	multiplier := 1 << min(errCount, 4) // 2× → 4× → 8× → 16×
	return base * time.Duration(multiplier)
}

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

// Run polls the account on the configured interval until ctx is cancelled.
func (f *Forwarder) Run(ctx context.Context) {
	f.logger.Info("starting forwarder",
		"account", f.account.Name,
		"protocol", f.account.Protocol,
		"host", f.account.Host,
		"interval", f.account.CheckInterval(),
	)

	// Run immediately on start, then on interval.
	f.poll()

	ticker := time.NewTicker(f.account.CheckInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			f.logger.Info("forwarder stopped", "account", f.account.Name)
			return
		case <-ticker.C:
			f.poll()
		}
	}
}

func (f *Forwarder) poll() {
	f.logger.Debug("polling", "account", f.account.Name)

	seenIDs := f.tracker.SeenIDs()
	emails, err := f.receiver.Fetch(seenIDs, f.account.GetProcessDays())
	if err != nil {
		f.logger.Error("fetch failed", "account", f.account.Name, "error", err)
		return
	}

	if len(emails) == 0 {
		f.logger.Debug("no new emails", "account", f.account.Name)
		return
	}

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

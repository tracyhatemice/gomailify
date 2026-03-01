package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/tracyhatemice/gomailify/internal/config"
	"github.com/tracyhatemice/gomailify/internal/dedup"
	"github.com/tracyhatemice/gomailify/internal/forwarder"
	"github.com/tracyhatemice/gomailify/internal/receiver"
	"github.com/tracyhatemice/gomailify/internal/sender"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to configuration file")
	dataDir := flag.String("data-dir", "data", "directory for persistent data (dedup state)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	logger := setupLogger(cfg.LogLevel)
	logger.Info("gomailify starting", "accounts", len(cfg.Accounts))

	smtp := sender.New(
		cfg.Sender.Host,
		cfg.Sender.Port,
		cfg.Sender.Username,
		cfg.Sender.Password,
		cfg.Sender.UseTLS,
		logger,
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var wg sync.WaitGroup

	for _, acct := range cfg.Accounts {
		recv, err := newReceiver(acct, logger)
		if err != nil {
			logger.Error("failed to create receiver", "account", acct.Name, "error", err)
			continue
		}

		dedupFile := filepath.Join(*dataDir, sanitize(acct.Name)+".seen")
		tracker, err := dedup.NewTracker(dedupFile)
		if err != nil {
			logger.Error("failed to create dedup tracker", "account", acct.Name, "error", err)
			continue
		}
		logger.Info("loaded dedup state", "account", acct.Name, "seen_count", tracker.Count())

		fwd := forwarder.New(acct, recv, smtp, tracker, logger)
		wg.Add(1)
		go func() {
			defer wg.Done()
			fwd.Run(ctx)
		}()
	}

	<-ctx.Done()
	logger.Info("shutting down, waiting for forwarders to finish...")

	// Force exit on second signal.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		logger.Warn("forced shutdown")
		os.Exit(1)
	}()

	wg.Wait()
	logger.Info("gomailify stopped")
}

func newReceiver(acct config.Account, logger *slog.Logger) (receiver.Receiver, error) {
	switch acct.Protocol {
	case "pop3":
		return receiver.NewPOP3(
			acct.Host, acct.Port,
			acct.Username, acct.Password,
			acct.UseTLS, logger,
		), nil
	case "imap":
		return receiver.NewIMAP(
			acct.Host, acct.Port,
			acct.Username, acct.Password,
			acct.UseTLS, acct.GetIMAPFolder(), logger,
		), nil
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", acct.Protocol)
	}
}

func setupLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func sanitize(name string) string {
	if name == "" {
		return "default"
	}
	out := make([]byte, 0, len(name))
	for _, b := range []byte(name) {
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '_' {
			out = append(out, b)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

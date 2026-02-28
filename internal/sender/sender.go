package sender

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/emersion/go-message/mail"
)

// Sender forwards raw email messages over SMTP.
type Sender struct {
	host     string
	port     int
	username string
	password string
	useTLS   bool
	logger   *slog.Logger
}

// New creates a new SMTP sender.
func New(host string, port int, username, password string, useTLS bool, logger *slog.Logger) *Sender {
	return &Sender{
		host:     host,
		port:     port,
		username: username,
		password: password,
		useTLS:   useTLS,
		logger:   logger,
	}
}

// Forward sends raw email content to the target address.
func (s *Sender) Forward(rawEmail []byte, to string, originalID string) error {
	addr := net.JoinHostPort(s.host, fmt.Sprintf("%d", s.port))

	// Parse the original email to extract the From header for envelope.
	from := s.username
	reader, err := mail.CreateReader(strings.NewReader(string(rawEmail)))
	if err == nil {
		defer reader.Close()
		if addrs, err := reader.Header.AddressList("From"); err == nil && len(addrs) > 0 {
			from = addrs[0].Address
		}
	}

	// Prepend forwarding headers to the raw email.
	forwardHeaders := fmt.Sprintf(
		"X-Forwarded-By: gomailify\r\nX-Original-Message-ID: %s\r\nX-Forwarded-Time: %s\r\n",
		originalID,
		time.Now().UTC().Format(time.RFC3339),
	)
	message := append([]byte(forwardHeaders), rawEmail...)

	var client *smtp.Client

	if s.useTLS {
		tlsConfig := &tls.Config{ServerName: s.host}
		conn, err := tls.Dial("tcp", addr, tlsConfig)
		if err != nil {
			return fmt.Errorf("smtp tls dial %s: %w", addr, err)
		}
		client, err = smtp.NewClient(conn, s.host)
		if err != nil {
			conn.Close()
			return fmt.Errorf("smtp new client: %w", err)
		}
	} else {
		client, err = smtp.Dial(addr)
		if err != nil {
			return fmt.Errorf("smtp dial %s: %w", addr, err)
		}
		// Try STARTTLS if available.
		if ok, _ := client.Extension("STARTTLS"); ok {
			tlsConfig := &tls.Config{ServerName: s.host}
			if err := client.StartTLS(tlsConfig); err != nil {
				s.logger.Warn("STARTTLS failed, continuing without TLS", "error", err)
			}
		}
	}
	defer client.Close()

	// Authenticate if credentials are provided.
	if s.username != "" && s.password != "" {
		auth := smtp.PlainAuth("", s.username, s.password, s.host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(message); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}

	return client.Quit()
}

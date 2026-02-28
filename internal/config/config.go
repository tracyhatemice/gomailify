package config

import (
	"fmt"
	"os"
	"time"

	"go.yaml.in/yaml/v4"
)

// Config is the top-level application configuration.
type Config struct {
	LogLevel string     `yaml:"log_level"`
	Sender   SMTP       `yaml:"sender"`
	Accounts []Account  `yaml:"accounts"`
}

// SMTP holds the outgoing mail server configuration.
type SMTP struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	UseTLS   bool   `yaml:"use_tls"`
}

// Account describes one monitored email account.
type Account struct {
	Name                 string `yaml:"name"`
	Protocol             string `yaml:"protocol"` // "pop3" or "imap"
	Host                 string `yaml:"host"`
	Port                 int    `yaml:"port"`
	Username             string `yaml:"username"`
	Password             string `yaml:"password"`
	UseTLS               bool   `yaml:"use_tls"`
	ForwardTo            string `yaml:"forward_to"`
	CheckIntervalSeconds int    `yaml:"check_interval_seconds"`
	ProcessDays          int    `yaml:"process_days"`
	IMAPFolder           string `yaml:"imap_folder"`
}

// CheckInterval returns the check interval as a time.Duration.
func (a *Account) CheckInterval() time.Duration {
	if a.CheckIntervalSeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(a.CheckIntervalSeconds) * time.Second
}

// GetProcessDays returns the number of days to look back, defaulting to 7.
func (a *Account) GetProcessDays() int {
	if a.ProcessDays <= 0 {
		return 7
	}
	return a.ProcessDays
}

// GetIMAPFolder returns the IMAP folder name, defaulting to "INBOX".
func (a *Account) GetIMAPFolder() string {
	if a.IMAPFolder == "" {
		return "INBOX"
	}
	return a.IMAPFolder
}

// Load reads and parses a YAML configuration file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := &Config{
		LogLevel: "info",
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Sender.Host == "" {
		return fmt.Errorf("sender.host is required")
	}
	if c.Sender.Port == 0 {
		return fmt.Errorf("sender.port is required")
	}
	if len(c.Accounts) == 0 {
		return fmt.Errorf("at least one account is required")
	}
	for i, a := range c.Accounts {
		label := a.Name
		if label == "" {
			label = fmt.Sprintf("#%d", i)
		}
		if a.Protocol != "pop3" && a.Protocol != "imap" {
			return fmt.Errorf("account %s: protocol must be pop3 or imap", label)
		}
		if a.Host == "" {
			return fmt.Errorf("account %s: host is required", label)
		}
		if a.Port == 0 {
			return fmt.Errorf("account %s: port is required", label)
		}
		if a.ForwardTo == "" {
			return fmt.Errorf("account %s: forward_to is required", label)
		}
	}
	return nil
}

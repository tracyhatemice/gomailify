# gomailify

A lightweight email forwarder that monitors POP3/IMAP accounts and forwards new emails to a destination address via SMTP. Designed as a self-hosted replacement for Gmail's "Check mail from other accounts" (POP) feature.

## Features

- **POP3 and IMAP** support with TLS/SSL
- **Multiple accounts** — monitor any number of source mailboxes concurrently
- **Configurable check interval** per account (in seconds)
- **Configurable process window** — only forward emails from the last N days
- **Dedup tracking** — persisted to disk, survives restarts, never forwards the same email twice
- **Graceful shutdown** on SIGINT/SIGTERM
- **Structured logging** via `log/slog` with configurable levels
- **Tiny Docker image** — built from `scratch` with UPX compression

## Quick Start

### Docker (recommended)

1. Create a `config.yaml` (see [Configuration](#configuration) below).

2. Run with Docker:

```bash
docker run -d \
  -v ./config.yaml:/app/config.yaml:ro \
  -v gomailify-data:/app/data \
  ghcr.io/tracyhatemice/gomailify
```

Or with Docker Compose — create a `compose.yaml`:

```yaml
services:
  gomailify:
    image: ghcr.io/tracyhatemice/gomailify:main
    container_name: gomailify
    restart: unless-stopped
    network_mode: bridge
    environment:
      TZ: "UTC"
    volumes:
      - ./config.yaml:/app/config.yaml:ro
      - gomailify-data:/app/data

volumes:
  gomailify-data:
    name: gomailify
```

```bash
docker compose up -d
```

### Build from source

```bash
go build -o gomailify ./cmd/gomailify
./gomailify --config config.yaml --data-dir ./data
```

## Configuration

All configuration is done via a single YAML file. Pass its path with `--config` (default: `config.yaml`).

```yaml
# Log level: debug, info, warn, error
log_level: info

# SMTP server used to send/forward emails
sender:
  host: smtp.gmail.com
  port: 465
  username: you@gmail.com
  password: your-app-password
  use_tls: true

# Source accounts to monitor
accounts:
  - name: work-pop3
    protocol: pop3
    host: pop.example.com
    port: 995
    username: alice@example.com
    password: secret
    use_tls: true
    forward_to: you@gmail.com
    check_interval_seconds: 120
    process_days: 7

  - name: personal-imap
    protocol: imap
    host: imap.example.com
    port: 993
    username: bob@example.com
    password: secret
    use_tls: true
    forward_to: you@gmail.com
    check_interval_seconds: 60
    process_days: 14
    imap_folder: INBOX
```

### Account fields

| Field | Required | Default | Description |
|---|---|---|---|
| `name` | yes | — | Label for logging and dedup file naming |
| `protocol` | yes | — | `pop3` or `imap` |
| `host` | yes | — | Mail server hostname |
| `port` | yes | — | Mail server port |
| `username` | no | — | Login username |
| `password` | no | — | Login password |
| `use_tls` | no | `false` | Use implicit TLS (STARTTLS is auto-negotiated when `false`) |
| `forward_to` | yes | — | Destination email address |
| `check_interval_seconds` | no | `60` | Polling interval |
| `process_days` | no | `7` | Only process emails from the last N days |
| `imap_folder` | no | `INBOX` | IMAP folder to monitor |

## CLI Flags

```
Usage: gomailify [flags]

  --config string     Path to configuration file (default "config.yaml")
  --data-dir string   Directory for persistent data (default "data")
```

## How It Works

1. On startup, each configured account spawns a goroutine that polls on its own interval.
2. Each poll fetches emails within the `process_days` window.
3. Message-IDs are checked against a per-account `.seen` file to skip duplicates.
4. New emails are forwarded as-is via SMTP with `X-Forwarded-By`, `X-Original-Message-ID`, and `X-Forwarded-Time` headers prepended.
5. Successfully forwarded Message-IDs are appended to the `.seen` file immediately.

## License

MIT

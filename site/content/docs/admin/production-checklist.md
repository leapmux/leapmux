---
title: "Production Checklist"
description: "Step-by-step checklist for deploying a LeapMux Hub for a team: network topology, storage, TLS reverse proxy, authentication, Workers, monitoring, and backups."
type: docs
weight: 1
---

Use this checklist to deploy a shared LeapMux **Hub** for a team. Each step links to the detailed guide for that component.

## Production network and trust topology

The Hub handles authentication, workspace metadata, and encrypted channel relay. Browsers and the `leapmux` command-line interface (CLI) reach the Hub through a TLS-terminating reverse proxy. Workers dial *out* to the same HTTPS endpoint from behind NAT or firewalls and never need inbound ports.

```mermaid
flowchart LR
    Clients["Browsers, Desktop & CLI"]
    Proxy["Reverse Proxy :443\n(TLS, /ws/ upgrades, Forwarded headers,\nblocks /metrics)"]
    Hub["Hub 127.0.0.1:4327\n(Auth, metadata, E2EE relay)"]
    DB[("SQL Database\n(PostgreSQL or SQLite)")]
    Key["encryption.key\n(mode 0600)"]
    Scraper["Host-local Prometheus\n(http://127.0.0.1:4327/metrics)"]
    Workers["Workers behind NAT\n(Outbound WebSocket only)"]

    Clients -->|"HTTPS + WSS :443"| Proxy
    Proxy -->|"HTTP/1.1 + WS"| Hub
    Workers -.->|"Outbound HTTPS + WSS :443"| Proxy
    Hub --> DB
    Hub --> Key
    Scraper -->|"Local scrape"| Hub
```

{{< callout type="warning" >}}
A Hub database supports **one running Hub process at a time**. The Hub holds a singleton runtime lease on the database, so do not deploy multiple Hub replicas against the same database.
{{< /callout >}}

## Deployment checklist

{{% steps %}}

### 1. Provision the Hub host and storage backend

Choose a dedicated host or container for the Hub and pick a storage backend from the [Storage Backend Decision Matrix](/docs/admin/configuration/#storage-backends-overview):

- **Small teams on one host:** embedded SQLite (`storage.type: sqlite`, default) works with zero external infrastructure.
- **Shared team deployments with managed backups:** **PostgreSQL** is the recommended external database.

Create `/etc/leapmux/hub.yaml`, binding the Hub to loopback only so external callers must pass through the reverse proxy:

```yaml
listen:
  - "127.0.0.1:4327"
data_dir: /var/lib/leapmux/hub
log_level: info

storage:
  type: postgres
  postgres:
    dsn: "postgres://leapmux:secret@db.internal:5432/leapmux?sslmode=require"
```

### 2. Start the Hub as a boot service

Install and start the Hub so it runs under a dedicated non-root user account and starts automatically on reboot:

```bash
leapmux daemon hub start --config /etc/leapmux/hub.yaml --startup=boot
```

Verify readiness with `leapmux daemon hub status --config /etc/leapmux/hub.yaml`. On first start, the Hub runs database migrations and generates `/var/lib/leapmux/hub/encryption.key` (mode `0600`). See [Starting and Stopping](/docs/admin/starting-and-stopping/).

### 3. Configure the TLS reverse proxy

Place Caddy, nginx, Traefik, HAProxy, or Apache in front of `127.0.0.1:4327` on a dedicated hostname or subdomain (sub-path prefixes are unsupported). Confirm the proxy:

- Terminates TLS on port `443` and redirects port `80` to HTTPS
- Forwards HTTP/1.1 WebSocket upgrades on `/ws/` with an inactivity timeout of at least 1 minute
- Sends `Host` and forwarding headers (`Forwarded` or `X-Forwarded-For` / `X-Forwarded-Proto`)
- Blocks `/metrics` from external clients so unauthenticated Prometheus metrics remain host-local

Use one of the tested configurations in [Reverse Proxy](/docs/admin/reverse-proxy/).

### 4. Bootstrap the first administrator

Create the first administrator account before opening sign-up:

- **In a browser:** open `https://hub.example.com` and complete `/setup`.
- **Offline on the Hub host (headless):** stop the Hub, run `leapmux recover create-admin <username> --config /etc/leapmux/hub.yaml`, then start the Hub again.

Sign in from your workstation CLI with admin scopes so you can apply live settings:

```bash
leapmux auth login --hub https://hub.example.com --scope "admin:read admin:settings admin:users admin:workers admin:apps"
```

### 5. Apply production Hub settings

Configure the public origin, secure cookies, trusted proxy addresses, and optional outbound mail. Hot settings take effect immediately on commit:

```bash
leapmux admin settings set public_url "https://hub.example.com"
leapmux admin settings set secure_cookies true
leapmux admin settings set trusted_proxy_ranges '["127.0.0.1", "::1"]'
leapmux admin settings set session_duration_seconds 86400
```

Enable SMTP if you want email verification, account recovery links, and Worker registration emails:

```bash
leapmux admin settings set smtp '{"host":"smtp.example.com","port":587,"username":"leapmux@example.com","from_address":"no-reply@example.com","tls_mode":"starttls"}'
leapmux admin settings set-secret smtp '{"password":"..."}'
```

Decide how users join: enable open sign-up (`leapmux admin settings set signup_enabled true`), create accounts with `leapmux admin user create`, or configure [Sign-in Providers](/docs/admin/sign-in-providers/) (GitHub, Google, Apple, or generic OIDC).

### 6. Register and start Workers

On each developer workstation or build server that will host coding agents and terminals:

1. Open the **Register worker** dialog in the Hub UI (**Workers → +**) to mint a single-use registration key.
2. Run the registration command and start the Worker service:

```bash
leapmux daemon worker register --hub https://hub.example.com --registration-key YOUR_KEY
leapmux daemon worker start --startup=boot
```

Verify that each Worker connects and compare its 4-word `key_fingerprint` startup log on first connection. See [Managing Workers](/docs/admin/managing-workers/).

### 7. Set up monitoring and backups

- **Metrics:** Scrape `http://127.0.0.1:4327/metrics` from a host-local Prometheus agent or private network tunnel. Watch `leapmux_sendq_pool_used_bytes`, `leapmux_sendq_pool_overcommits_total`, `leapmux_connections_refused_total`, and `leapmux_active_workers`. See [Configuration Reference — Prometheus metrics](/docs/admin/configuration-reference/#prometheus-metrics).
- **Backups:** Back up the **Hub database and `encryption.key` together** as an encrypted matched pair. Losing `encryption.key` makes encrypted Hub columns unrecoverable. Back up each Worker's `state.json` if you want to preserve Worker identities across host rebuilds. See [Encryption & Data](/docs/admin/encryption-and-data/#backup-and-restore).

### 8. Verify the live deployment

Run these checks before inviting your team:

- `curl -sI https://hub.example.com/ | head -1` returns `HTTP/2 200`.
- `curl -s -o /dev/null -w "%{http_code}\n" https://hub.example.com/metrics` returns `404` or `403`.
- `curl -s http://127.0.0.1:4327/metrics | head -5` on the Hub host returns Prometheus metrics.
- Opening an agent or terminal tab on a registered Worker succeeds and streams live output.

{{% /steps %}}

{{< callout type="info" >}}
Review [Security & Authentication](/docs/admin/security-and-auth/) before exposing the Hub to external users to confirm your rate limits, captcha policy, and passkey origin settings.
{{< /callout >}}

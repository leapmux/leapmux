---
title: "Administration"
description: "Host LeapMux for a team: production setup, run modes, configuration, reverse proxies, Workers, sign-in providers, security, encryption, and offline recovery."
type: docs
weight: 4
sidebar:
  open: true
---

For administrators hosting LeapMux for a team: production deployment, run modes, service lifecycle, HTTPS through a reverse proxy, configuration, Worker and account management, encryption, security, and offline recovery.

## Which admin surface to use

LeapMux exposes three administration surfaces. Choose the surface that matches whether the Hub is running and which domain you are changing.

| Admin domain | Web UI (**Preferences → Administration**) | `leapmux admin` (Hub running) | `leapmux recover` (Hub stopped / offline) |
| --- | --- | --- | --- |
| **Hub settings** (public URL, cookies, proxies, SMTP, timeouts, limits, captcha, rate limits) | Yes | `leapmux admin settings`, `captcha`, `rate-limit` | — |
| **Network listeners** (live TCP/IPC status, solo extra addresses) | Yes (**Network access**) | `leapmux admin network status`, `settings set extra_listen_addresses` | — |
| **Sign-in providers** (GitHub, Google, Apple, OIDC) | — | `leapmux admin idp` (add, list, enable, disable, rotate-secret, remove) | — |
| **Hub-wide apps** (register, verify, elevation, retire) | Yes (**Hub-wide Apps**) | `leapmux admin app` | — |
| **Users** (create, update, admin role, password reset, delete) | Yes (**Users**) | `leapmux admin user` (elevated CLI credential) | `create-admin` (first admin only), `reset-password` (break-glass) |
| **Sessions & tokens** (sessions, API tokens, delegation tokens) | — | `leapmux admin session`, `api-token`, `delegation-token` | — |
| **Workers & registration keys** | Sidebar (**Workers**) | `leapmux admin worker`, `worker reg-key` | — |
| **Encryption keys & pepper** (`encryption.key`) | — | — | `leapmux recover encryption-key` (`rotate`, `reencrypt`, `remove`, `rotate-pepper`) |
| **Database migrations & path** | — | — | `leapmux recover db` (`path`, `version`, `migrate`) |

## Guides and references

{{< cards >}}
  {{< card link="/docs/admin/production-checklist/" title="Production Checklist" icon="check-circle" subtitle="Step-by-step team Hub rollout, network topology, and verification." >}}
  {{< card link="/docs/admin/running-leapmux/" title="Running LeapMux" icon="server" subtitle="Run modes, capability matrix, ports, data directories, and Docker." >}}
  {{< card link="/docs/admin/starting-and-stopping/" title="Starting and Stopping" icon="play" subtitle="Start, stop, and check LeapMux as an OS service at login or boot." >}}
  {{< card link="/docs/admin/configuration/" title="Configuration Guide" icon="cog" subtitle="Precedence, deployment profiles, listen addresses, storage matrix, and defaults." >}}
  {{< card link="/docs/admin/configuration-reference/" title="Configuration Reference" icon="document-text" subtitle="Complete reference of Hub and Worker keys, flags, storage backends, and all 21 metrics." >}}
  {{< card link="/docs/admin/reverse-proxy/" title="Reverse Proxy" icon="globe-alt" subtitle="Serve LeapMux over HTTPS behind Caddy, nginx, Traefik, HAProxy, or Apache." >}}
  {{< card link="/docs/admin/managing-workers/" title="Managing Workers" icon="chip" subtitle="Register Workers, handle reconnects and deregistration, and verify TOFU key pins." >}}
  {{< card link="/docs/admin/sign-in-providers/" title="Sign-in Providers" icon="key" subtitle="Configure GitHub, Google, Apple, or generic OIDC single sign-on." >}}
  {{< card link="/docs/admin/app-authorization/" title="App Authorization" icon="shield-check" subtitle="Register OAuth 2.1 apps, control scopes, and vouch for trusted apps." >}}
  {{< card link="/docs/admin/security-and-auth/" title="Security & Authentication" icon="finger-print" subtitle="Trust model, session elevation, passkeys, OAuth security, rate limits, and revocation." >}}
  {{< card link="/docs/admin/encryption-and-data/" title="Encryption & Data" icon="lock-closed" subtitle="At-rest encryption, key and pepper rotation, browser storage, and backups." >}}
  {{< card link="/docs/admin/cryptography/" title="Cryptography & Key Architecture" icon="key" subtitle="Hybrid post-quantum Noise handshake, SLH-DSA Worker keys, transport hardening, and pinning." >}}
  {{< card link="/docs/admin/admin-cli/" title="Admin CLI" icon="terminal" subtitle="Administer a running Hub over RPC: settings, users, sessions, IdPs, and tokens." >}}
  {{< card link="/docs/admin/recover/" title="Recovery" icon="support" subtitle="Offline break-glass: first-admin bootstrap, password reset, keys, and database utilities." >}}
{{< /cards >}}

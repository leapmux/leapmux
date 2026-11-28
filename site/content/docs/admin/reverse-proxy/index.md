---
title: "Reverse Proxy"
description: "Serve a LeapMux Hub over HTTPS behind Caddy, nginx, Traefik, HAProxy, or Apache: proxy comparison, required settings, /metrics protection, and tested configs."
type: docs
weight: 6
---

The Hub does not terminate TLS itself. To serve LeapMux over HTTPS, place a reverse proxy in front of the Hub. This page compares five common proxies, lists every requirement, explains why `/metrics` stays host-local, and provides a ready-to-use configuration for each proxy.

## Reverse proxy comparison

| Proxy | Automatic TLS | Minimum version | Request body-size handling | WebSocket notes | How `/metrics` is blocked |
| --- | --- | --- | --- | --- | --- |
| **Caddy** | Built-in (Let's Encrypt / ZeroSSL) | Caddy 2 | Unlimited by default | Automatic on `reverse_proxy` | `respond /metrics 404` |
| **nginx** | External (for example Certbot) | 1.25.1 (`http2 on;`) | Must set `client_max_body_size 0` (1 MiB default rejects bulk RPCs) | Explicit `proxy_http_version 1.1` + `Upgrade` / `Connection` map | `location = /metrics { return 404; }` |
| **Traefik** | Built-in via certificate resolvers | Traefik v3 | Unlimited by default | Automatic on HTTP router; default read timeout allows long streams | `` !Path(`/metrics`) `` in router rule |
| **HAProxy** | External (combined PEM chain + key) | HAProxy 2.4+ | Unlimited by default | HTTP/1.1 backend carries upgrades; set `timeout tunnel` ≥ 1m | `http-request deny deny_status 404 if { path,url_dec /metrics }` |
| **Apache httpd** | External (for example Certbot) | 2.4.47 (`upgrade=websocket`) | Unlimited by default | Single `ProxyPass / ... upgrade=websocket` handles HTTP and WS | `ProxyPass /metrics !` + `Require all denied` |

## What the proxy must carry

The Hub serves two kinds of traffic on one port:

| Traffic | Path | How the proxy forwards it |
| --- | --- | --- |
| Worker streams, web app live updates, and encrypted channels | `/ws/` | HTTP/1.1 WebSocket upgrades that stay open for hours |
| Web app assets, API calls, Worker registration, and `leapmux` CLI | Every other path | HTTP/1.1 or HTTP/2 |

The Hub pings each WebSocket, including Worker streams, every 10 seconds. Configure an inactivity timeout of at least 1 minute so regular pings keep healthy streams alive.

Every proxy must also:

- Forward WebSocket upgrades on `/ws/`.
- Preserve `Host`, strip any client-supplied `Forwarded` and `X-Forwarded-*` headers, and set forwarding headers (`Forwarded`, or `X-Forwarded-For` and `X-Forwarded-Proto`) from the proxy's own peer connection.
- Serve LeapMux at the root of its own hostname or subdomain — sub-path prefixes such as `https://example.com/leapmux` are unsupported and rejected by `public_url`.
- **Block `/metrics` from external clients.** The Hub exposes Prometheus metrics at `/metrics` without authentication so host-local scrapers can read them. All five proxy examples below deny `/metrics` externally; scrape `http://127.0.0.1:4327/metrics` directly on the Hub host instead.

## Configure the Hub

{{% steps %}}

### Bind the Hub to loopback

Configure `listen` in `hub.yaml` so only the local reverse proxy can reach the Hub:

```yaml
listen:
  - "127.0.0.1:4327"
```

In Docker, publish the port on loopback only with `-p 127.0.0.1:4327:4327`. See [Production Hub behind a reverse proxy](/docs/admin/configuration/#production-hub-behind-a-reverse-proxy).

### Set the public URL, secure cookies, and trusted proxies

Apply the three reverse-proxy settings on the running Hub. All three are `hot` settings and take effect immediately on commit with no restart:

```bash
leapmux admin settings set public_url "https://hub.example.com"
leapmux admin settings set secure_cookies true
leapmux admin settings set trusted_proxy_ranges '["127.0.0.1", "::1"]'
```

- `secure_cookies true` switches session cookies to `__Host-` prefixed secure cookies, signing out existing plain-cookie sessions. Set it during initial deployment.
- `trusted_proxy_ranges` trusts a proxy on the same host in this example; replace it with the actual IP or CIDR range for a remote proxy. The Hub ignores forwarding headers from untrusted peers. See [Trusted reverse proxies](/docs/admin/configuration/#trusted-reverse-proxies).

### Register Workers against the external HTTPS URL

Register every standalone Worker with `--hub https://hub.example.com`. Each Worker saves that URL in `state.json` and connects out to it over HTTPS/WSS, requiring no inbound ports of its own.

{{% /steps %}}

{{< callout type="warning" >}}
If you put `cloudflare` or `cloudfront` in `trusted_proxy_ranges`, LeapMux trusts the bundled IP ranges for the entire provider. Restrict origin traffic to your own distribution or require authenticated origin requests, and configure every proxy to strip client-supplied forwarding headers. LeapMux does not download updated provider ranges automatically.
{{< /callout >}}

## Proxy configurations

Each configuration below serves `hub.example.com` on port `443`, redirects port `80` to HTTPS, blocks `/metrics` externally, strips client-supplied `Forwarded` headers (and overwrites `X-Forwarded-For` with the real peer address), and forwards all other traffic — including WebSocket upgrades — to `127.0.0.1:4327` over HTTP/1.1. Replace the hostname, certificate paths, and Hub address with your own. The first comment line of each file shows its install path.

{{< tabs >}}
{{< tab name="nginx" >}}
Requires nginx 1.25.1 or later for the `http2 on;` directive.

`client_max_body_size 0` lets the Hub enforce its own payload ceiling on unary requests; nginx's 1 MiB default would reject valid bulk requests.

{{< config-file file="nginx.conf" lang="nginx" >}}
{{< /tab >}}

{{< tab name="Caddy" >}}
Caddy obtains and renews TLS certificates automatically. `respond /metrics 404` keeps Prometheus metrics host-local while `reverse_proxy` forwards HTTP and WebSocket upgrades with forwarding headers.

{{< config-file file="Caddyfile" lang="caddyfile" >}}
{{< /tab >}}

{{< tab name="Traefik" >}}
The static configuration defines HTTP and HTTPS entry points. Traefik's default request-read timeout allows long-lived WebSocket streams without overrides.

{{< config-file file="traefik.yml" lang="yaml" >}}

The dynamic configuration excludes `/metrics` from the public router and forwards all other requests and WebSocket upgrades to the Hub.

{{< config-file file="traefik-dynamic.yml" lang="yaml" >}}

To obtain certificates from Let's Encrypt automatically, define a certificate resolver in `traefik.yml`, set `tls.certResolver` on the router, and remove the static `tls.certificates` block.
{{< /tab >}}

{{< tab name="HAProxy" >}}
`alpn h2,http/1.1` on the `bind` line offers HTTP/2 to browsers while forwarding HTTP/1.1 and WebSocket upgrades to the Hub. The PEM file must contain the full certificate chain followed by the private key.

{{< config-file file="haproxy.cfg" lang="haproxy" >}}
{{< /tab >}}

{{< tab name="Apache" >}}
Requires Apache httpd 2.4.47 or later for `upgrade=websocket`. Enable the required modules and site on Debian or Ubuntu:

```bash
sudo a2enmod ssl http2 proxy proxy_http headers
sudo a2ensite leapmux
sudo systemctl reload apache2
```

{{< config-file file="apache.conf" lang="apache" >}}
{{< /tab >}}
{{< /tabs >}}

## Check the setup

{{% steps %}}

### Verify HTTPS, HTTP/2, and `/metrics` protection

```bash
# Confirm HTTP/2 and 200 OK for the web app:
curl -sI https://hub.example.com/ | head -1

# Confirm /metrics is blocked externally (expect 404 or 403):
curl -s -o /dev/null -w "%{http_code}\n" https://hub.example.com/metrics

# On the Hub host, confirm local scraping works:
curl -s http://127.0.0.1:4327/metrics | head -5
```

### Register and start a Worker through the proxy

See [Register a Worker](/docs/admin/managing-workers/#register-a-worker) to mint a registration key, then test end-to-end connectivity through TLS:

```bash
leapmux daemon worker register --hub https://hub.example.com --registration-key <key>
leapmux daemon worker start
```

Registration prints `Worker registered.`, and the Worker appears online in the sidebar **Workers** section.

{{% /steps %}}

If any check fails, see [Troubleshooting — Reverse proxy](/docs/reference/troubleshooting/workers-and-network/#worker-fails-through-a-reverse-proxy).

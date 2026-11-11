---
title: "Reverse Proxy"
description: "Serve a LeapMux Hub over HTTPS behind nginx, Caddy, Traefik, HAProxy, or Apache: what the proxy must carry, a tested configuration for each, and how to check it."
type: docs
weight: 3
---

The Hub does not terminate TLS itself. To serve LeapMux over HTTPS, put a reverse proxy in front of the Hub. This page states what the proxy must carry, gives a configuration for five common proxies, and shows how to check the result.

## What the proxy must carry

The Hub serves two kinds of traffic on one port:

| Traffic | Path | How the proxy forwards it |
|---|---|---|
| The Worker's stream, web app live updates, and encrypted channels | `/ws/` | HTTP/1.1 WebSocket upgrades that stay open for hours |
| The web app, its API calls, Worker registration, and `leapmux control` | Every other path | HTTP/1.1 or HTTP/2 |

The Hub pings each WebSocket, including the Worker's stream, every 10 seconds. Use an inactivity timeout of one minute or longer. Regular probes keep a healthy connection active through that timeout.

The proxy must also do these things:

- Forward WebSocket upgrades.
- Pass the `Host` header, and set `X-Forwarded-For` and `X-Forwarded-Proto`.
- Serve LeapMux at the root of its own host name.

{{< callout type="warning" >}}
LeapMux does not support a sub-path such as `https://example.com/leapmux`, and the Hub refuses such a `public_url`. Give LeapMux its own host name or subdomain.
{{< /callout >}}

## Configure the Hub

1. Make the Hub listen on loopback only, so that clients can reach it only through the proxy. Set `listen: "127.0.0.1:4327"` in the Hub's configuration file; see [Configuration](/docs/admin/configuration/#production-hub-behind-a-reverse-proxy). In Docker, publish the port on loopback with `-p 127.0.0.1:4327:4327`.
2. Set `public_url` to the external HTTPS URL: `leapmux control admin settings set public_url "https://hub.example.com"`.
3. Enable secure cookies with `leapmux control admin settings set secure_cookies true`. This signs out current sessions, so configure it during initial setup.
4. Trust the proxy's address with `leapmux control admin settings set trusted_proxy_ranges '["127.0.0.1", "::1"]'`. This example trusts a proxy on the same machine over IPv4 or IPv6. Use the actual address or range for a remote proxy. The Hub ignores the forwarding headers of a proxy that it does not trust.
5. Register each Worker with `--hub` set to the same external `https://` address. A Worker keeps using the URL that it registered with. Workers always initiate outbound connections, so they need no inbound ports of their own.

The Hub applies the three settings within about 30 seconds. No restart is needed. For the supported proxy address formats and forwarding headers, see [Trusted reverse proxies](/docs/admin/configuration/#trusted-reverse-proxies).

{{< callout type="warning" >}}
If you use `cloudflare` or `cloudfront` in `trusted_proxy_ranges`, those values trust the bundled ranges for the entire provider. Restrict access to your distribution or use authenticated origin requests. Configure every proxy to remove client-supplied forwarding headers or append only verified values. LeapMux does not download updated provider ranges automatically.
{{< /callout >}}

## Proxy configurations

Each configuration below does these things:

- It serves `hub.example.com` on port 443, and redirects port 80 to it.
- It reaches the Hub at `127.0.0.1:4327` on the same machine.
- It forwards all traffic to the Hub over HTTP/1.1, including WebSocket upgrades.

The repository's `task test-reverse-proxies` harness verifies registration, connection, and transfers larger than 4 MiB before and after a 200-second stability window. It checks the web app's event WebSocket for that window too. All five examples pass those checks. A separate HAProxy run verifies a 15-second inactivity timeout for 60 seconds.

Replace the host name, the certificate paths, and the Hub's address with your own. The title of each file states where the file goes.

### nginx

The configuration requires nginx 1.25.1 or later, for the `http2` directive.

`client_max_body_size 0` lets the Hub enforce its own limit on unary requests. nginx's default limit of 1 MiB would reject bulk requests that the Hub accepts. The Worker's stream uses a WebSocket upgrade.

{{< config-file file="nginx.conf" lang="nginx" >}}

### Caddy

Caddy obtains and renews the certificate itself. One `reverse_proxy` forwards API calls and WebSocket upgrades to the Hub and sets the forwarding headers.

{{< config-file file="Caddyfile" lang="caddyfile" >}}

### Traefik

The static configuration defines the entry points. Traefik's default request-read timeout permits the WebSocket stream; no timeout override is needed.

{{< config-file file="traefik.yml" lang="yaml" >}}

The dynamic configuration uses one router and one HTTP upstream for the web app, API calls, and WebSocket upgrades.

{{< config-file file="traefik-dynamic.yml" lang="yaml" >}}

To obtain the certificate from Let's Encrypt, define a certificate resolver in the static configuration, set it as `tls.certResolver` on the router, and remove the `tls.certificates` section. The Traefik documentation describes certificate resolvers.

### HAProxy

`alpn h2,http/1.1` on the `bind` line offers HTTP/2 to browsers. The HTTP/1.1 upstream carries API calls and WebSocket upgrades. The certificate file holds the certificate chain followed by the private key.

{{< config-file file="haproxy.cfg" lang="haproxy" >}}

### Apache httpd

The configuration requires Apache httpd 2.4.47 or later, for `upgrade=websocket`. One HTTP upstream carries all traffic, including the Worker's WebSocket. Enable the modules and the site on Debian or Ubuntu:

```bash
sudo a2enmod ssl http2 proxy proxy_http headers
sudo a2ensite leapmux
sudo systemctl reload apache2
```

{{< config-file file="apache.conf" lang="apache" >}}

## Check the setup

1. Check that the proxy serves the web app over HTTP/2:

   ```bash
   curl -sI https://hub.example.com/ | head -1
   ```

   The first line is `HTTP/2 200`.
2. Register a Worker through the proxy, and start it. See [Managing Workers](/docs/admin/managing-workers/#register-a-worker).

   ```bash
   leapmux worker register --hub https://hub.example.com --registration-key <key>
   leapmux worker start
   ```

   The registration prints `Worker registered.`, and the Worker appears in the sidebar **Workers** section.

If a step fails, see [A Worker cannot register or stay connected through a reverse proxy](/docs/reference/troubleshooting/#a-worker-cannot-register-or-stay-connected-through-a-reverse-proxy).

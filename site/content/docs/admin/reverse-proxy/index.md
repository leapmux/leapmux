---
title: "Reverse Proxy"
description: "Serve a LeapMux Hub over HTTPS behind nginx, Caddy, Traefik, HAProxy, or Apache: what the proxy must carry, a tested configuration for each, and how to check it."
type: docs
weight: 3
---

The Hub does not terminate TLS itself. To serve LeapMux over HTTPS, put a reverse proxy in front of the Hub. This page states what the proxy must carry, gives a configuration for five common proxies, and shows how to check the result.

## What the proxy must carry

The Hub serves three kinds of traffic on one port:

| Traffic | Path | How the proxy forwards it |
|---|---|---|
| The Worker's service: registration and the Worker's stream | `/leapmux.v1.WorkerConnectorService/` | Cleartext HTTP/2 (h2c), as one stream that lasts as long as the Worker runs |
| The WebSockets of the web app and of the end-to-end encrypted channels | `/ws/` | An HTTP/1.1 upgrade that stays open for hours |
| The web app, its API calls, and `leapmux control` | Every other path | HTTP/1.1 or HTTP/2 |

The Worker's stream needs more than a typical web application does. The proxy must meet these requirements for it:

- **Accept HTTP/2 from clients.** A Worker connects to the proxy over HTTPS with HTTP/2 only.
- **Forward the service to the Hub over HTTP/2.** The Worker's stream sends and receives at the same time, which HTTP/1.1 cannot do. The Hub accepts cleartext HTTP/2 (h2c) and HTTP/1.1 on the same port, so the Hub needs no setting for this.
- **Stream the request without buffering it, and without a limit on its size or its total duration.** A Worker sends all of its traffic in the body of one request, for as long as the Worker runs. A proxy that buffers the request never delivers the Worker's first message. A limit on the body size, or on the time to read the whole request, ends the Worker's connection.

A timeout for inactivity is safe. The Worker and the Hub exchange a message every few seconds, and the Hub probes each WebSocket every 30 seconds. A timeout of one minute or longer therefore never ends a healthy connection.

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
- It sends `/leapmux.v1.WorkerConnectorService/` to the Hub over cleartext HTTP/2, and everything else over HTTP/1.1.

Replace the host name, the certificate paths, and the Hub's address with your own. The title of each file states where the file goes.

### nginx

The configuration requires nginx 1.25.1 or later, for the `http2` directive.

- `grpc_pass` is nginx's HTTP/2 proxy. It sends the Worker's service to the Hub over HTTP/2 and streams the request, although the Worker does not use the gRPC protocol. `proxy_pass` does neither by default: it forwards over HTTP/1.1, and it buffers the whole request before it sends it. nginx 1.29 can carry the service with `proxy_pass` too, but only with all of `proxy_http_version 2`, `proxy_request_buffering off`, and `proxy_buffering off`. `grpc_pass` needs none of them.
- `client_max_body_size 0` removes nginx's limit of 1 MiB on a request body. nginx applies that limit to the Worker's whole stream, so the default ends the Worker's connection after its first mebibyte of traffic.

{{< config-file file="nginx.conf" lang="nginx" >}}

### Caddy

Caddy obtains and renews the certificate itself. The `h2c://` scheme sends the Worker's service to the Hub over cleartext HTTP/2. Caddy forwards WebSocket upgrades and sets the forwarding headers without further configuration.

{{< config-file file="Caddyfile" lang="caddyfile" >}}

### Traefik

The static configuration defines the entry points. The `readTimeout: 0` line is necessary: Traefik limits the time to read a whole request to 60 seconds by default, which ends a Worker's connection every minute.

{{< config-file file="traefik.yml" lang="yaml" >}}

The dynamic configuration routes the Worker's service to an `h2c://` server, and everything else to an `http://` server. Traefik gives a router with a longer rule a higher priority, so the Worker's router wins for its path.

{{< config-file file="traefik-dynamic.yml" lang="yaml" >}}

To obtain the certificate from Let's Encrypt, define a certificate resolver in the static configuration, set it as `tls.certResolver` on both routers, and remove the `tls.certificates` section. The Traefik documentation describes certificate resolvers.

### HAProxy

`proto h2` on the `server` line makes HAProxy talk to the Hub over cleartext HTTP/2 for the Worker's service. `alpn h2,http/1.1` on the `bind` line offers HTTP/2 to clients. The certificate file holds the certificate chain followed by the private key.

{{< config-file file="haproxy.cfg" lang="haproxy" >}}

### Apache httpd

The configuration requires Apache httpd 2.4.47 or later, for `upgrade=websocket`. The `h2c://` scheme of `mod_proxy_http2` sends the Worker's service to the Hub over cleartext HTTP/2. Enable the modules and the site on Debian or Ubuntu:

```bash
sudo a2enmod ssl http2 proxy proxy_http proxy_http2 headers
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

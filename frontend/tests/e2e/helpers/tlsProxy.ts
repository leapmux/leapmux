/**
 * Local HTTPS reverse proxy for E2E: terminates TLS in front of a cleartext
 * hub, mirroring the documented production shape (nginx/Caddy → Hub over
 * HTTP). The browser origin is `https://localhost:<port>`, so
 * `window.isSecureContext` is true and Connect sends an https Origin —
 * exactly what the ALTCHA secure-context gate must treat as enabled.
 */
import type { Buffer } from 'node:buffer'
import type { IncomingMessage, RequestOptions } from 'node:http'
import type { Server as HttpsServer } from 'node:https'
import type { Socket } from 'node:net'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import https from 'node:https'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { findFreePort } from './server'

export interface TlsProxyHandle {
  /** Browser-facing base URL (`https://localhost:<port>`). */
  url: string
  /** Cleartext hub the proxy forwards to. */
  hubUrl: string
  close: () => Promise<void>
}

function mintSelfSignedCert(): { key: Buffer, cert: Buffer, dir: string } {
  const dir = mkdtempSync(join(tmpdir(), 'leapmux-e2e-tls-'))
  const keyPath = join(dir, 'key.pem')
  const certPath = join(dir, 'cert.pem')
  const result = spawnSync('openssl', [
    'req',
    '-x509',
    '-newkey',
    'rsa:2048',
    '-keyout',
    keyPath,
    '-out',
    certPath,
    '-days',
    '1',
    '-nodes',
    '-subj',
    '/CN=localhost',
    // SANs so Chromium accepts the name under modern TLS checks once
    // ignoreHTTPSErrors is set (and so the CN alone is not the only binding).
    '-addext',
    'subjectAltName=DNS:localhost,IP:127.0.0.1',
  ], { encoding: 'utf-8' })
  if (result.status !== 0) {
    rmSync(dir, { recursive: true, force: true })
    throw new Error(`openssl failed to mint a self-signed cert: ${result.stderr || result.stdout}`)
  }
  // Touch a marker so leftover dirs are obvious when debugging.
  writeFileSync(join(dir, 'README'), 'leapmux e2e TLS proxy material; safe to delete\n')
  return {
    key: readFileSync(keyPath),
    cert: readFileSync(certPath),
    dir,
  }
}

function forwardHeaders(req: IncomingMessage, targetHost: string): http.OutgoingHttpHeaders {
  // Drop hop-by-hop headers; rewrite Host to the cleartext hub so the
  // Hub's absolute redirects (if any) stay on the backend address while
  // the browser Origin stays on the https front door.
  const headers: http.OutgoingHttpHeaders = { ...req.headers, host: targetHost }
  delete headers.connection
  delete headers['keep-alive']
  delete headers['proxy-connection']
  delete headers['transfer-encoding']
  delete headers.upgrade
  return headers
}

/**
 * Start an HTTPS terminator that reverse-proxies every request (and
 * WebSocket upgrade) to `hubUrl`. Caller must close the handle.
 */
export async function startTlsProxy(hubUrl: string): Promise<TlsProxyHandle> {
  const target = new URL(hubUrl)
  if (target.protocol !== 'http:') {
    throw new Error(`startTlsProxy expects a cleartext hub URL, got ${hubUrl}`)
  }
  const targetHost = target.host
  const targetPort = Number(target.port || 80)
  const targetHostname = target.hostname

  const { key, cert, dir } = mintSelfSignedCert()
  const port = await findFreePort()

  const server: HttpsServer = https.createServer({ key, cert }, (req, res) => {
    const opts: RequestOptions = {
      hostname: targetHostname,
      port: targetPort,
      path: req.url,
      method: req.method,
      headers: forwardHeaders(req, targetHost),
    }
    const proxyReq = http.request(opts, (proxyRes) => {
      res.writeHead(proxyRes.statusCode ?? 502, proxyRes.headers)
      proxyRes.pipe(res)
    })
    proxyReq.on('error', (err) => {
      if (!res.headersSent)
        res.writeHead(502)
      res.end(`tls proxy error: ${err.message}`)
    })
    req.pipe(proxyReq)
  })

  server.on('upgrade', (req, socket: Socket, head: Buffer) => {
    const opts: RequestOptions = {
      hostname: targetHostname,
      port: targetPort,
      path: req.url,
      method: 'GET',
      headers: {
        ...req.headers,
        host: targetHost,
        connection: 'Upgrade',
        upgrade: 'websocket',
      },
    }
    const proxyReq = http.request(opts)
    proxyReq.on('upgrade', (proxyRes, proxySocket, proxyHead) => {
      const lines = [`HTTP/1.1 101 Switching Protocols`]
      for (const [name, value] of Object.entries(proxyRes.headers)) {
        if (value === undefined)
          continue
        if (Array.isArray(value)) {
          for (const v of value)
            lines.push(`${name}: ${v}`)
        }
        else {
          lines.push(`${name}: ${value}`)
        }
      }
      lines.push('', '')
      socket.write(lines.join('\r\n'))
      if (proxyHead.length)
        socket.write(proxyHead)
      if (head.length)
        proxySocket.write(head)
      proxySocket.pipe(socket)
      socket.pipe(proxySocket)
      proxySocket.on('error', () => socket.destroy())
      socket.on('error', () => proxySocket.destroy())
    })
    proxyReq.on('error', () => socket.destroy())
    proxyReq.end()
  })

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(port, '127.0.0.1', () => resolve())
  })

  const url = `https://localhost:${port}`
  // Node's fetch rejects the self-signed cert; probe with verification off.
  const deadline = Date.now() + 30_000
  let ready = false
  while (!ready && Date.now() < deadline) {
    try {
      await new Promise<void>((resolve, reject) => {
        https.get(url, { rejectUnauthorized: false }, (res) => {
          res.resume()
          resolve()
        }).on('error', reject)
      })
      ready = true
    }
    catch {
      await new Promise(r => setTimeout(r, 25))
    }
  }
  if (!ready)
    throw new Error(`TLS proxy at ${url} did not become ready`)

  return {
    url,
    hubUrl,
    close: async () => {
      await new Promise<void>((resolve, reject) => {
        server.close(err => (err ? reject(err) : resolve()))
      }).catch(() => {
        // Already closed / never listening.
      })
      rmSync(dir, { recursive: true, force: true })
    },
  }
}

/**
 * An HTTPS proxy for end-to-end (E2E) tests.
 * Transport Layer Security (TLS) ends at the proxy. The proxy forwards cleartext HTTP to the Hub.
 * The browser uses a secure origin, which enables the ALTCHA secure-context check.
 */
import type { Buffer } from 'node:buffer'
import type { ClientRequest, IncomingMessage, RequestOptions, ServerResponse } from 'node:http'
import type { Server as HttpsServer } from 'node:https'
import type { Duplex } from 'node:stream'
import { spawnSync } from 'node:child_process'
import { readFileSync, rmSync, writeFileSync } from 'node:fs'
import http from 'node:http'
import https from 'node:https'
import { join } from 'node:path'
import { cleanupOnFailure, withCleanup } from './cleanup'
import { createTestDirectory } from './runDirectory'

export interface TlsProxyHandle {
  /** The browser URL, which uses https://localhost:<port>. */
  url: string
  /** The cleartext Hub URL. */
  hubUrl: string
  close: () => Promise<void>
}

async function mintSelfSignedCert(): Promise<{ key: Buffer, cert: Buffer, dir: string }> {
  const dir = createTestDirectory('leapmux-e2e-tls-')
  return cleanupOnFailure(async () => {
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
      // Subject Alternative Names identify both loopback names that the tests use.
      '-addext',
      'subjectAltName=DNS:localhost,IP:127.0.0.1',
    ], { encoding: 'utf-8' })
    if (result.error)
      throw new Error('OpenSSL could not create the test certificate.', { cause: result.error })
    if (result.status !== 0)
      throw new Error(`OpenSSL failed to create the test certificate: ${result.stderr || result.stdout}`)
    writeFileSync(join(dir, 'README'), 'LeapMux test TLS certificates. This directory contains no user secrets.\n')
    return { key: readFileSync(keyPath), cert: readFileSync(certPath), dir }
  }, async () => {
    rmSync(dir, { recursive: true, force: true })
  })
}

function forwardHeaders(incoming: IncomingMessage['headers']): http.OutgoingHttpHeaders {
  const headers: http.OutgoingHttpHeaders = { ...incoming }
  const connection = incoming.connection
  for (const entry of Array.isArray(connection) ? connection : [connection ?? '']) {
    for (const token of entry.split(','))
      delete headers[token.trim().toLowerCase()]
  }
  for (const name of ['connection', 'keep-alive', 'proxy-connection', 'proxy-authenticate', 'proxy-authorization', 'te', 'trailer', 'transfer-encoding', 'upgrade'])
    delete headers[name]
  return headers
}

// The Hub readiness helpers use the same 30-second startup limit.
const TLS_READINESS_TIMEOUT_MS = 30_000

/** Check one HTTPS response. Reject a refusal or a transport failure without a retry. */
function verifyTlsReadiness(url: string): Promise<void> {
  return new Promise((resolve, reject) => {
    let completed = false
    const request = https.get(url, { agent: false, rejectUnauthorized: false }, (response) => {
      response.on('error', finish)
      response.once('end', () => {
        const status = response.statusCode ?? 0
        finish(status >= 200 && status < 400
          ? undefined
          : new Error(`The TLS proxy readiness request returned HTTP ${status}.`))
      })
      response.resume()
    })
    const deadline = setTimeout(() => {
      finish(new Error('The TLS proxy readiness request exceeded its startup deadline.'))
    }, TLS_READINESS_TIMEOUT_MS)
    request.once('error', finish)

    function finish(error?: Error): void {
      if (completed)
        return
      completed = true
      clearTimeout(deadline)
      request.destroy()
      if (error)
        reject(error)
      else
        resolve()
    }
  })
}

function reportProxyFailure(response: ServerResponse, error: Error): void {
  if (response.destroyed)
    return
  if (response.headersSent) {
    // A partial body cannot carry a new HTTP failure status. Abort that response.
    response.destroy()
    return
  }
  response.writeHead(502)
  response.end(`The TLS proxy request failed: ${error.message}`)
}

/** Start a private HTTPS listener that forwards HTTP and WebSocket requests. */
export async function startTlsProxy(hubUrl: string): Promise<TlsProxyHandle> {
  const target = new URL(hubUrl)
  if (target.protocol !== 'http:')
    throw new Error(`startTlsProxy expects a cleartext hub URL, got ${hubUrl}`)

  const { key, cert, dir } = await mintSelfSignedCert()
  // A private HTTP Agent prevents one proxy's shutdown from closing another proxy's pooled connections.
  const upstreamAgent = new http.Agent({ keepAlive: true })
  const sockets = new Set<Duplex>()
  const requests = new Set<ClientRequest>()
  let server: HttpsServer | undefined
  let closing: Promise<void> | undefined

  function trackSocket(socket: Duplex): void {
    sockets.add(socket)
    socket.once('close', () => sockets.delete(socket))
  }

  function upstreamRequest(options: RequestOptions): ClientRequest {
    const request = http.request({ ...options, agent: upstreamAgent })
    requests.add(request)
    request.once('close', () => requests.delete(request))
    return request
  }

  function close(): Promise<void> {
    closing ??= withCleanup(async () => {
      for (const request of requests)
        request.destroy()
      for (const socket of sockets)
        socket.destroy()
      upstreamAgent.destroy()
      const activeServer = server
      if (!activeServer)
        return
      await new Promise<void>((resolve, reject) => {
        activeServer.close((error) => {
          // A failed bind leaves a server that never listened.
          if (error && 'code' in error && error.code === 'ERR_SERVER_NOT_RUNNING') {
            resolve()
            return
          }
          if (error)
            reject(error)
          else
            resolve()
        })
      })
    }, async () => {
      rmSync(dir, { recursive: true, force: true })
    })
    return closing
  }

  return cleanupOnFailure(async () => {
    const activeServer = https.createServer({ key, cert }, (req, res) => {
      const upstream = upstreamRequest({
        hostname: target.hostname,
        port: Number(target.port || 80),
        path: req.url,
        method: req.method,
        // The upstream Host identifies the Hub. Preserve the browser's Origin.
        headers: { ...forwardHeaders(req.headers), host: target.host },
      })
      upstream.on('response', (response) => {
        res.writeHead(response.statusCode ?? 502, forwardHeaders(response.headers))
        response.on('error', error => reportProxyFailure(res, error))
        response.pipe(res)
      })
      upstream.on('error', error => reportProxyFailure(res, error))
      req.on('aborted', () => upstream.destroy())
      res.once('close', () => {
        if (!res.writableFinished)
          upstream.destroy()
      })
      req.pipe(upstream)
    })
    server = activeServer
    activeServer.on('connection', trackSocket)

    activeServer.on('upgrade', (req, socket: Duplex, head: Buffer) => {
      trackSocket(socket)
      const upstream = upstreamRequest({
        hostname: target.hostname,
        port: Number(target.port || 80),
        path: req.url,
        method: 'GET',
        headers: { ...forwardHeaders(req.headers), host: target.host, connection: 'Upgrade', upgrade: 'websocket' },
      })
      upstream.on('upgrade', (response, peer, peerHead) => {
        trackSocket(peer)
        const lines = ['HTTP/1.1 101 Switching Protocols', 'Connection: Upgrade', 'Upgrade: websocket']
        for (const [name, value] of Object.entries(forwardHeaders(response.headers))) {
          if (value === undefined)
            continue
          for (const item of Array.isArray(value) ? value : [value])
            lines.push(`${name}: ${item}`)
        }
        lines.push('', '')
        socket.write(lines.join('\r\n'))
        if (peerHead.length)
          socket.write(peerHead)
        if (head.length)
          peer.write(head)
        peer.pipe(socket)
        socket.pipe(peer)
        peer.on('error', () => socket.destroy())
        socket.on('error', () => peer.destroy())
        peer.once('close', () => socket.destroy())
        socket.once('close', () => peer.destroy())
      })
      upstream.on('response', (response) => {
        response.resume()
        socket.destroy()
      })
      upstream.on('error', () => socket.destroy())
      socket.once('close', () => upstream.destroy())
      upstream.end()
    })

    await new Promise<void>((resolve, reject) => {
      activeServer.once('error', reject)
      activeServer.listen(0, '127.0.0.1', () => {
        activeServer.off('error', reject)
        resolve()
      })
    })
    const address = activeServer.address()
    if (!address || typeof address === 'string')
      throw new Error('The TLS proxy listener has no TCP address.')
    const url = `https://localhost:${address.port}`
    await verifyTlsReadiness(url)
    return { url, hubUrl, close }
  }, close)
}

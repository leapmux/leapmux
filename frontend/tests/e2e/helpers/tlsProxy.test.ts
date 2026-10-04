import type { ClientRequest, IncomingMessage, Server } from 'node:http'
import type { Socket } from 'node:net'
import type { Duplex } from 'node:stream'
import { Buffer } from 'node:buffer'
import { mkdirSync, mkdtempSync, readdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import https from 'node:https'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { deferred } from '~/test-support/async'
import { createTestDirectory } from './runDirectory'
import { findFreePort } from './server'
import { startTlsProxy } from './tlsProxy'

const state = vi.hoisted(() => ({ root: '' }))
vi.mock('./server', async (original) => {
  const actual = await original<typeof import('./server')>()
  return {
    ...actual,
    getGlobalState: () => ({ tmpDir: state.root }),
    findFreePort: vi.fn(actual.findFreePort),
  }
})
vi.mock('./runDirectory', async (original) => {
  const actual = await original<typeof import('./runDirectory')>()
  return { ...actual, createTestDirectory: vi.fn(actual.createTestDirectory) }
})

type TestServer = Server | https.Server

let servers: Set<TestServer>
let tlsServers: Set<https.Server>
let sockets: Set<Duplex>
let serverSockets: Map<TestServer, Set<Socket>>
let upstreamRequests: ClientRequest[]

async function listen(server: TestServer): Promise<number> {
  servers.add(server)
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      server.off('error', reject)
      resolve()
    })
  })
  return boundPort(server)
}

function boundPort(server: TestServer): number {
  const address = server.address()
  if (!address || typeof address === 'string')
    throw new Error('The controlled server has no TCP address.')
  return address.port
}

async function request(url: string, options: https.RequestOptions = {}): Promise<{ status: number, body: string }> {
  return new Promise((resolve, reject) => {
    const req = https.request(url, { agent: false, rejectUnauthorized: false, ...options }, (response) => {
      const parts: Buffer[] = []
      response.on('data', part => parts.push(Buffer.from(part)))
      response.once('error', reject)
      response.once('end', () => resolve({ status: response.statusCode ?? 0, body: Buffer.concat(parts).toString('utf8') }))
    })
    req.once('error', reject)
    req.end(options.method === 'POST' ? 'REQUEST42' : undefined)
  })
}

async function target(handler: (request: IncomingMessage, response: http.ServerResponse) => void = (_request, response) => response.end('REPLY42')): Promise<string> {
  return `http://127.0.0.1:${await listen(http.createServer(handler))}`
}

beforeEach(() => {
  vi.mocked(findFreePort).mockReset()
  vi.mocked(createTestDirectory).mockReset()
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  state.root = mkdtempSync(join(scratch, 'tls-proxy-test-'))
  servers = new Set()
  tlsServers = new Set()
  sockets = new Set()
  serverSockets = new Map()
  upstreamRequests = []
  const originalRequest = http.request
  vi.spyOn(http, 'request').mockImplementation((...args: Parameters<typeof http.request>) => {
    const request = originalRequest(...args)
    upstreamRequests.push(request)
    return request
  })
  const createServer = https.createServer
  vi.spyOn(https, 'createServer').mockImplementation((...args: Parameters<typeof https.createServer>) => {
    const server = createServer(...args)
    servers.add(server)
    tlsServers.add(server)
    const owned = new Set<Socket>()
    serverSockets.set(server, owned)
    server.on('connection', (socket) => {
      sockets.add(socket)
      owned.add(socket)
    })
    return server
  })
})

afterEach(async () => {
  vi.restoreAllMocks()
  for (const socket of sockets)
    socket.destroy()
  await Promise.all([...servers].map(async (server) => {
    server.closeAllConnections()
    if (!server.listening)
      return
    await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()))
  }))
  rmSync(state.root, { recursive: true, force: true })
})

describe('startTlsProxy', () => {
  it('binds its final listener directly while another process owns the provisional port', async () => {
    const occupied = await listen(http.createServer())
    vi.mocked(findFreePort).mockResolvedValueOnce(occupied)
    const hubUrl = await target()
    const proxy = await startTlsProxy(hubUrl)
    try {
      expect(new URL(proxy.url).port).not.toBe(String(occupied))
      expect(await request(proxy.url)).toEqual({ status: 200, body: 'REPLY42' })
      expect(findFreePort).not.toHaveBeenCalled()
    }
    finally {
      await proxy.close()
    }
    expect(readdirSync(state.root)).toEqual([])
  })

  it('keeps concurrent proxies on distinct final ports', async () => {
    const hubUrl = await target()
    const started = await Promise.allSettled([startTlsProxy(hubUrl), startTlsProxy(hubUrl)])
    const proxies = started.flatMap(result => result.status === 'fulfilled' ? [result.value] : [])
    try {
      expect(started.every(result => result.status === 'fulfilled')).toBe(true)
      expect(new Set(proxies.map(proxy => proxy.url)).size).toBe(2)
      expect(await Promise.all(proxies.map(proxy => request(proxy.url)))).toEqual([
        { status: 200, body: 'REPLY42' },
        { status: 200, body: 'REPLY42' },
      ])
    }
    finally {
      await Promise.all(proxies.map(proxy => proxy.close()))
    }
  })

  it('keeps the second proxy usable after the first proxy closes its connections', async () => {
    const hubUrl = await target()
    const first = await startTlsProxy(hubUrl)
    const second = await startTlsProxy(hubUrl)
    try {
      expect(await request(first.url)).toEqual({ status: 200, body: 'REPLY42' })
      expect(await request(second.url)).toEqual({ status: 200, body: 'REPLY42' })
      await first.close()
      expect(await request(second.url)).toEqual({ status: 200, body: 'REPLY42' })
    }
    finally {
      await Promise.all([first.close(), second.close()])
    }
  })

  it('reports an upstream connection failure through HTTP 502', async () => {
    const hubUrl = await target((incoming, response) => {
      if (incoming.url === '/')
        response.end('ready')
      else
        incoming.socket.destroy()
    })
    const proxy = await startTlsProxy(hubUrl)
    try {
      const response = await request(`${proxy.url}/failure`)
      expect(response.status).toBe(502)
      expect(response.body).toContain('The TLS proxy request failed:')
    }
    finally {
      await proxy.close()
    }
  })

  it('proves HTTPS readiness with one response before it returns the proxy', async () => {
    let requests = 0
    const hubUrl = await target((_incoming, response) => {
      requests++
      response.end('ready')
    })
    const proxy = await startTlsProxy(hubUrl)
    try {
      expect(requests).toBe(1)
    }
    finally {
      await proxy.close()
    }
  })

  it('returns a readiness refusal and removes its certificate files', async () => {
    const hubUrl = await target((_incoming, response) => {
      response.writeHead(503)
      response.end('not ready')
    })
    const result = await startTlsProxy(hubUrl).then(
      proxy => ({ status: 'ready' as const, proxy }),
      (error: unknown) => ({ status: 'failed' as const, error }),
    )
    try {
      expect(result.status).toBe('failed')
      if (result.status === 'failed')
        expect(result.error).toMatchObject({ message: expect.stringMatching(/readiness.*503/) })
    }
    finally {
      if (result.status === 'ready')
        await result.proxy.close()
    }
    expect(readdirSync(state.root)).toEqual([])
  })

  it('removes the header tokens that Connection lists in both directions', async () => {
    let received: http.IncomingHttpHeaders | undefined
    const hubUrl = await target((incoming, response) => {
      received = incoming.headers
      response.setHeader('Connection', 'keep-alive, x-response-only')
      response.setHeader('X-response-only', 'RESPONSESECRET42')
      response.end('ready')
    })
    const proxy = await startTlsProxy(hubUrl)
    try {
      const headers = await new Promise<http.IncomingHttpHeaders>((resolve, reject) => {
        const client = https.get(proxy.url, {
          agent: false,
          rejectUnauthorized: false,
          headers: { 'connection': 'keep-alive, x-request-only', 'x-request-only': 'REQUESTSECRET42' },
        }, (response) => {
          response.resume()
          response.once('end', () => resolve(response.headers))
          response.once('error', reject)
        })
        client.once('error', reject)
      })
      expect(received?.['x-request-only']).toBeUndefined()
      expect(headers['x-response-only']).toBeUndefined()
    }
    finally {
      await proxy.close()
    }
  })

  it('cancels the upstream response after the browser closes its partial response', async () => {
    let heldResponse: http.ServerResponse | undefined
    const hubUrl = await target((incoming, response) => {
      if (incoming.url !== '/held') {
        response.end('ready')
        return
      }
      heldResponse = response
      response.writeHead(200)
      response.write('FIRST42')
    })
    const proxy = await startTlsProxy(hubUrl)
    const downstreamClosed = deferred<void>()
    const server = [...tlsServers][0]
    if (!server)
      throw new Error('The controlled TLS server is absent.')
    server.on('request', (incoming, response) => {
      if (incoming.url === '/held')
        response.once('close', () => downstreamClosed.resolve())
    })
    const firstChunk = deferred<void>()
    const client = https.get(`${proxy.url}/held`, { agent: false, rejectUnauthorized: false }, (response) => {
      response.once('data', () => firstChunk.resolve())
      response.on('error', error => firstChunk.reject(error))
    })
    client.on('error', error => firstChunk.reject(error))
    try {
      await firstChunk.promise
      const upstream = upstreamRequests.find(request => request.path === '/held')
      if (!upstream)
        throw new Error('The controlled upstream request is absent.')
      const destroy = vi.spyOn(upstream, 'destroy')
      client.destroy()
      await downstreamClosed.promise
      expect(destroy).toHaveBeenCalled()
    }
    finally {
      client.destroy()
      heldResponse?.end()
      await proxy.close()
    }
  })

  it('closes the browser socket when the upstream refuses a WebSocket upgrade', async () => {
    const hubUrl = await target((_incoming, response) => response.end('no upgrade'))
    const proxy = await startTlsProxy(hubUrl)
    const server = [...tlsServers][0]
    if (!server)
      throw new Error('The controlled TLS server is absent.')
    let downstream: Duplex | undefined
    server.on('upgrade', (_incoming, socket) => {
      downstream = socket
    })
    const refused = deferred<void>()
    const originalRequest = vi.mocked(http.request).getMockImplementation()
    if (!originalRequest)
      throw new Error('The controlled HTTP request factory is absent.')
    vi.mocked(http.request).mockImplementation((...args: Parameters<typeof http.request>) => {
      const request = originalRequest(...args)
      request.once('response', (response) => {
        response.resume()
        refused.resolve()
      })
      return request
    })
    const client = https.request(`${proxy.url}/declined`, {
      agent: false,
      rejectUnauthorized: false,
      headers: { connection: 'Upgrade', upgrade: 'websocket' },
    })
    const clientErrors: Error[] = []
    client.on('error', error => clientErrors.push(error))
    client.end()
    try {
      await refused.promise
      expect(downstream?.destroyed).toBe(true)
    }
    finally {
      client.destroy()
      await proxy.close()
    }
    for (const error of clientErrors)
      expect(error).toMatchObject({ code: 'ECONNRESET' })
  })

  it('forwards the body and browser origin while replacing the upstream Host', async () => {
    const observed: { body: string | undefined, origin: string | undefined, host: string | undefined, url: string | undefined } = {
      body: undefined,
      origin: undefined,
      host: undefined,
      url: undefined,
    }
    const hubUrl = await target((incoming, response) => {
      const parts: Buffer[] = []
      incoming.on('data', part => parts.push(Buffer.from(part)))
      incoming.once('end', () => {
        observed.body = Buffer.concat(parts).toString('utf8')
        observed.origin = incoming.headers.origin
        observed.host = incoming.headers.host
        observed.url = incoming.url
        response.writeHead(201)
        response.end('FORWARDED42')
      })
    })
    const proxy = await startTlsProxy(hubUrl)
    try {
      expect(await request(`${proxy.url}/native/path?value=42`, { method: 'POST', headers: { origin: proxy.url } }))
        .toEqual({ status: 201, body: 'FORWARDED42' })
      expect(observed).toEqual({ body: 'REQUEST42', origin: proxy.url, host: new URL(hubUrl).host, url: '/native/path?value=42' })
    }
    finally {
      await proxy.close()
    }
  })

  it('releases its certificate directory when certificate setup throws', async () => {
    const directory = join(state.root, 'controlled-certificate-directory')
    mkdirSync(join(directory, 'README'), { recursive: true })
    vi.mocked(createTestDirectory).mockReturnValueOnce(directory)
    await expect(startTlsProxy('http://127.0.0.1:30001')).rejects.toMatchObject({ code: 'EISDIR' })
    expect(readdirSync(state.root)).toEqual([])
  })

  it('releases its certificate directory when TLS server creation throws', async () => {
    const failed = new Error('The controlled TLS server creation failed.')
    vi.mocked(https.createServer).mockImplementationOnce(() => {
      throw failed
    })
    await expect(startTlsProxy('http://127.0.0.1:30001')).rejects.toBe(failed)
    expect(readdirSync(state.root)).toEqual([])
  })

  it('releases its certificate directory after a final listener bind failure', async () => {
    const failed = new Error('The controlled final listener bind failed.')
    const createServer = vi.mocked(https.createServer).getMockImplementation()
    if (!createServer)
      throw new Error('The controlled TLS server factory is absent.')
    vi.mocked(https.createServer).mockImplementationOnce((...args: Parameters<typeof https.createServer>) => {
      const server = createServer(...args)
      vi.spyOn(server, 'listen').mockImplementation(() => {
        queueMicrotask(() => server.emit('error', failed))
        return server
      })
      return server
    })
    await expect(startTlsProxy('http://127.0.0.1:30001')).rejects.toBe(failed)
    expect(readdirSync(state.root)).toEqual([])
  })

  it('reports an unexpected close failure and still removes certificate files', async () => {
    const proxy = await startTlsProxy(await target())
    const server = [...tlsServers][0]
    if (!server)
      throw new Error('The controlled TLS server is absent.')
    const failed = new Error('The controlled listener close failed.')
    vi.spyOn(server, 'close').mockImplementationOnce((callback) => {
      callback?.(failed)
      return server
    })
    await expect(proxy.close()).rejects.toBe(failed)
    expect(readdirSync(state.root)).toEqual([])
  })

  it('shares one close operation across concurrent callers', async () => {
    const proxy = await startTlsProxy(await target())
    const server = [...tlsServers][0]
    if (!server)
      throw new Error('The controlled TLS server is absent.')
    const close = vi.spyOn(server, 'close')
    await Promise.all([proxy.close(), proxy.close()])
    expect(close).toHaveBeenCalledTimes(1)
    expect(readdirSync(state.root)).toEqual([])
  })

  it.each(['https://localhost:30001', 'ftp://localhost:30001', 'file:///private/file'])('rejects the non-HTTP target %s before it creates certificates', async (hubUrl) => {
    await expect(startTlsProxy(hubUrl)).rejects.toThrow('cleartext hub URL')
    expect(createTestDirectory).not.toHaveBeenCalled()
    expect(readdirSync(state.root)).toEqual([])
  })
  it('forwards an upgraded connection and closes its live socket before waiting for server shutdown', async () => {
    const hub = http.createServer((_incoming, response) => response.end('ready'))
    hub.on('upgrade', (_incoming, socket) => {
      sockets.add(socket)
      socket.write('HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n')
      socket.once('data', bytes => socket.write(`REPLY${bytes.toString('utf8')}`))
    })
    const proxy = await startTlsProxy(`http://127.0.0.1:${await listen(hub)}`)
    const client = await new Promise<Socket>((resolve, reject) => {
      const req = https.request(proxy.url, { agent: false, rejectUnauthorized: false, headers: { connection: 'Upgrade', upgrade: 'websocket' } })
      req.once('error', reject)
      req.once('upgrade', (_response, socket: Socket) => {
        sockets.add(socket)
        resolve(socket)
      })
      req.end()
    })
    const reply = new Promise<string>((resolve, reject) => {
      client.once('data', bytes => resolve(bytes.toString('utf8')))
      client.once('error', reject)
    })
    client.write('FRAME42')
    expect(await reply).toBe('REPLYFRAME42')
    const server = [...tlsServers][0]
    if (!server)
      throw new Error('The controlled TLS server is absent.')
    const owned = [...serverSockets.get(server) ?? []]
    expect(owned.length).toBeGreaterThan(0)
    const closing = proxy.close()
    try {
      expect(owned.every(socket => socket.destroyed), 'close must stop live upgraded connections before it waits').toBe(true)
    }
    finally {
      for (const socket of sockets)
        socket.destroy()
      await closing
    }
    expect(readdirSync(state.root)).toEqual([])
  })
})

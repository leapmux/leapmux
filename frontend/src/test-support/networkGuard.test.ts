import { request as httpRequest } from 'node:http'
import { Worker } from 'node:worker_threads'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { installNetworkGuard } from './networkGuard'

/** The JSDOM instance that vitest's jsdom environment exposes. */
declare const jsdom: { reconfigure: (settings: { url?: string }) => void }

/**
 * A local server that answers every request with HTTP 401, as a development
 * server or a hub with no session does. It counts each request and each socket
 * upgrade, so a test can prove that nothing reached it.
 */
interface UnauthorizedServer {
  origin: string
  host: string
  hits: () => Promise<number>
  close: () => Promise<void>
}

/**
 * The server runs in a worker thread, never on the test's own thread. A
 * synchronous XMLHttpRequest blocks the test's thread until the answer comes,
 * so a server on that thread cannot answer. Without the guard, the request
 * then hangs for two minutes instead of failing at once.
 *
 * jsdom's XMLHttpRequest enforces CORS. Without the CORS header, a request from
 * the document origin reads status 0, not the 401 that the server sent.
 */
const SERVER_SOURCE = `
const { createServer } = require('node:http')
const { parentPort } = require('node:worker_threads')
let hits = 0
const server = createServer((req, res) => {
  hits++
  req.resume()
  res.writeHead(401, { 'content-type': 'application/json', 'access-control-allow-origin': '*' })
  res.end(JSON.stringify({ code: 'unauthenticated', message: 'no session' }))
})
server.on('upgrade', (_req, socket) => {
  hits++
  socket.destroy()
})
server.listen(0, '127.0.0.1', () => parentPort.postMessage({ kind: 'listening', port: server.address().port }))
parentPort.on('message', ({ kind, id }) => {
  if (kind === 'hits')
    parentPort.postMessage({ kind, id, hits })
  if (kind === 'close') {
    server.closeAllConnections()
    server.close(() => parentPort.postMessage({ kind, id }))
  }
})
`

interface ServerReply { kind: string, id?: number, port?: number, hits?: number }

const openServers: UnauthorizedServer[] = []

async function startUnauthorizedServer(): Promise<UnauthorizedServer> {
  const worker = new Worker(SERVER_SOURCE, { eval: true })
  const pending = new Map<number, (reply: ServerReply) => void>()
  let nextId = 0
  // Each wait rejects on a worker error, and removes its listener when it
  // settles. With no listener left, a later worker error is uncaught, and
  // vitest reports it.
  const listening = new Promise<number>((resolve, reject) => {
    worker.once('error', reject)
    worker.on('message', (reply: ServerReply) => {
      if (reply.kind === 'listening') {
        worker.off('error', reject)
        resolve(reply.port!)
      }
      else {
        pending.get(reply.id!)?.(reply)
      }
    })
  })
  const ask = (kind: 'hits' | 'close') => new Promise<ServerReply>((resolve, reject) => {
    const id = nextId++
    const fail = (err: Error) => {
      pending.delete(id)
      reject(err)
    }
    worker.once('error', fail)
    pending.set(id, (reply) => {
      pending.delete(id)
      worker.off('error', fail)
      resolve(reply)
    })
    worker.postMessage({ kind, id })
  })
  const port = await listening
  const started: UnauthorizedServer = {
    origin: `http://127.0.0.1:${port}`,
    host: `127.0.0.1:${port}`,
    hits: async () => (await ask('hits')).hits!,
    close: async () => {
      await ask('close')
      await worker.terminate()
    },
  }
  openServers.push(started)
  return started
}

/**
 * One request through `node:http`, which the guard does not wrap. A test sends
 * it first, to prove that the server answers, so that a later count of one
 * means that the guarded request never arrived.
 */
function postDirectly(server: UnauthorizedServer): Promise<number> {
  return new Promise((resolve, reject) => {
    const req = httpRequest(`${server.origin}/probe`, { method: 'POST' }, (res) => {
      res.resume()
      res.on('end', () => resolve(res.statusCode ?? 0))
    })
    req.on('error', reject)
    req.end()
  })
}

/** Moves the jsdom document onto `server`, as if the suite's origin were there. */
function moveDocumentTo(server: UnauthorizedServer): void {
  jsdom.reconfigure({ url: `${server.origin}/` })
}

function thrownBy(action: () => unknown): unknown {
  try {
    action()
  }
  catch (err) {
    return err
  }
  throw new Error('the action did not throw')
}

const originalUrl = window.location.href

afterEach(async () => {
  vi.unstubAllGlobals()
  vi.resetModules()
  jsdom.reconfigure({ url: originalUrl })
  await Promise.all(openServers.splice(0).map(server => server.close()))
})

describe('installNetworkGuard', () => {
  it('refuses a fetch to the document host before the request leaves the process', async () => {
    const server = await startUnauthorizedServer()
    expect(await postDirectly(server)).toBe(401)
    moveDocumentTo(server)

    const refused = fetch(`${server.origin}/leapmux.v1.UserService/GetTimeouts`, { method: 'POST' })

    await expect(refused).rejects.toBeInstanceOf(TypeError)
    await expect(refused).rejects.toThrow(`refused POST ${server.origin}/leapmux.v1.UserService/GetTimeouts`)
    expect(await server.hits()).toBe(1)
  })

  it('refuses a fetch that a Request object carries', async () => {
    const server = await startUnauthorizedServer()
    moveDocumentTo(server)

    const refused = fetch(new Request(`${server.origin}/x`, { method: 'PUT' }))

    await expect(refused).rejects.toThrow(`refused PUT ${server.origin}/x`)
    expect(await server.hits()).toBe(0)
  })

  it('refuses a fetch to the document host on any scheme', async () => {
    const server = await startUnauthorizedServer()
    moveDocumentTo(server)

    await expect(fetch(`https://${server.host}/x`)).rejects.toThrow(`refused GET https://${server.host}/x`)
    expect(await server.hits()).toBe(0)
  })

  it('lets a fetch to another host reach its server', async () => {
    // The tests under tests/e2e/helpers start mock servers on ephemeral ports
    // and send real requests to them. The document stays on its own host.
    const server = await startUnauthorizedServer()
    expect(window.location.host).not.toBe(server.host)

    const response = await fetch(`${server.origin}/v1/chat/completions`, { method: 'POST' })
    await response.arrayBuffer()

    expect(response.status).toBe(401)
    expect(await server.hits()).toBe(1)
  })

  it('passes a URL with no host to the real fetch', async () => {
    const response = await fetch('data:text/plain,hello')

    expect(await response.text()).toBe('hello')
  })

  it('refuses a relative URL, which a browser resolves against the document', async () => {
    const server = await startUnauthorizedServer()
    moveDocumentTo(server)

    const refused = fetch('/leapmux.v1.UserService/GetTimeouts', { method: 'POST' })

    await expect(refused).rejects.toThrow(`refused POST ${server.origin}/leapmux.v1.UserService/GetTimeouts`)
    expect(await server.hits()).toBe(0)
  })

  it('reads the document host at each call', async () => {
    const first = await startUnauthorizedServer()
    const second = await startUnauthorizedServer()
    moveDocumentTo(first)
    await expect(fetch(`${first.origin}/x`)).rejects.toThrow('network guard')

    moveDocumentTo(second)
    const response = await fetch(`${first.origin}/x`)
    await response.arrayBuffer()

    expect(response.status).toBe(401)
    expect(await first.hits()).toBe(1)
    await expect(fetch(`${second.origin}/x`)).rejects.toThrow('network guard')
    expect(await second.hits()).toBe(0)
  })

  it('refuses a synchronous XMLHttpRequest for a path on the document host with a NetworkError', async () => {
    // The shape that Playwright's bundled source-map-support sends: a GET for
    // a file-system path, which resolves against the document origin.
    const server = await startUnauthorizedServer()
    moveDocumentTo(server)
    const xhr = new XMLHttpRequest()
    xhr.open('GET', '/Users/someone/node_modules/playwright/lib/index.js', false)

    const error = thrownBy(() => xhr.send(null))

    expect(error).toBeInstanceOf(DOMException)
    expect((error as DOMException).name).toBe('NetworkError')
    expect((error as DOMException).message).toContain(`${server.origin}/Users/someone/node_modules/playwright/lib/index.js`)
    expect(await server.hits()).toBe(0)
  })

  it('forgets a refused target when the same XMLHttpRequest opens another host', async () => {
    const server = await startUnauthorizedServer()
    const xhr = new XMLHttpRequest()
    xhr.open('GET', `${window.location.origin}/x`)
    xhr.open('GET', `${server.origin}/y`)
    const settled = new Promise<number>((resolve) => {
      xhr.addEventListener('loadend', () => resolve(xhr.status))
    })

    xhr.send()

    expect(await settled).toBe(401)
    expect(await server.hits()).toBe(1)
  })

  it('refuses a WebSocket to the document host', async () => {
    const server = await startUnauthorizedServer()
    moveDocumentTo(server)

    expect(() => new WebSocket(`ws://${server.host}/ws/channel`)).toThrow(`refused the WebSocket to ws://${server.host}/ws/channel`)
    expect(await server.hits()).toBe(0)
  })

  it('lets a WebSocket to another host reach its server', async () => {
    // The server ends each upgrade at once, so the socket closes on its own.
    const server = await startUnauthorizedServer()
    const socket = new WebSocket(`ws://${server.host}/ws/channel`)
    const closed = new Promise<void>((resolve) => {
      socket.addEventListener('close', () => resolve())
    })

    expect(socket).toBeInstanceOf(WebSocket)
    await closed
    expect(await server.hits()).toBe(1)
  })

  it('keeps the WebSocket constants and instanceof', () => {
    expect(WebSocket.OPEN).toBe(1)
    expect(WebSocket.CLOSED).toBe(3)
    expect(Object.create(WebSocket.prototype)).toBeInstanceOf(WebSocket)
  })

  it('yields to a fetch stub, and returns when the stub goes', async () => {
    const stub = vi.fn<typeof fetch>().mockResolvedValue(new Response('stubbed'))
    vi.stubGlobal('fetch', stub)

    const response = await fetch(`${window.location.origin}/x`)

    expect(await response.text()).toBe('stubbed')
    vi.unstubAllGlobals()
    await expect(fetch(`${window.location.origin}/x`)).rejects.toThrow('network guard')
  })

  it('installs once, however often it runs', () => {
    const fetchBefore = globalThis.fetch
    const openBefore = XMLHttpRequest.prototype.open
    const webSocketBefore = globalThis.WebSocket

    installNetworkGuard()

    expect(globalThis.fetch).toBe(fetchBefore)
    expect(XMLHttpRequest.prototype.open).toBe(openBefore)
    expect(globalThis.WebSocket).toBe(webSocketBefore)
  })

  // The failure the guard exists for, end to end. A 401 from the document host
  // reaches the error interceptor as `Code.Unauthenticated`, and the
  // interceptor reports an ended session -- which `AuthContext` answers with a
  // sign-out in the middle of a test.
  it('keeps a 401 on the document host from reporting an ended session through the transport', async () => {
    const server = await startUnauthorizedServer()
    moveDocumentTo(server)
    // The transport reads its base URL from the document when the module
    // evaluates, so evaluate it again for the moved document.
    vi.resetModules()
    const transport = await import('~/api/transport')
    const sessionEnded = vi.fn()
    transport.setOnAuthError(sessionEnded)

    await transport.loadTimeouts()

    expect(sessionEnded).not.toHaveBeenCalled()
    expect(await server.hits()).toBe(0)
  })
})

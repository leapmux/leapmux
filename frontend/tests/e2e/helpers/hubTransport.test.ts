import { once } from 'node:events'
import { createServer } from 'node:http'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { E2E_BROWSER_HOST, E2E_HUB_HOST, fetchHub, hubTransportUrl } from './hubTransport'

afterEach(() => vi.unstubAllGlobals())

describe('E2E_BROWSER_HOST', () => {
  it('matches the browser session-cookie domain', () => {
    expect(E2E_BROWSER_HOST).toBe('localhost')
  })
})

describe('hubTransportUrl', () => {
  it.each(['http', 'ws'])('connects plain %s to the private IPv4 listener', (scheme) => {
    expect(hubTransportUrl(`${scheme}://localhost:43210/path?q=value`)).toBe(`${scheme}://127.0.0.1:43210/path?q=value`)
  })

  it.each([
    'https://localhost:43210/path',
    'wss://localhost:43210/path',
    'http://localhost.example:43210/path',
    'http://127.0.0.1:43210/path',
    'http://[::1]:43210/path',
    'http://remote.example/path',
  ])('preserves an explicit address, remote hostname, or secure server name: %s', (url) => {
    expect(hubTransportUrl(url)).toBe(url)
  })

  it('preserves port zero and the maximum port without a truthiness fallback', () => {
    for (const port of [0, 65535]) {
      expect(hubTransportUrl(`http://localhost:${port}/`)).toBe(`http://127.0.0.1:${port}/`)
    }
  })

  it.each(['', 'relative/path', 'http://localhost:-1/', 'http://localhost:65536/'])('rejects an invalid URL: %j', (url) => {
    expect(() => hubTransportUrl(url)).toThrow(TypeError)
  })
})

describe('fetchHub', () => {
  it('returns a rejected promise for an invalid URL without starting a request', async () => {
    const transport = vi.fn(async () => new Response())
    vi.stubGlobal('fetch', transport)
    let pending: Promise<Response> | undefined
    expect(() => {
      pending = fetchHub('')
    }).not.toThrow()
    await expect(pending).rejects.toThrow(TypeError)
    expect(transport).not.toHaveBeenCalled()
  })

  it('reaches a real IPv4 server with its IPv4 authority and explicit session cookie', async () => {
    const server = createServer((request, response) => {
      response.setHeader('Content-Type', 'application/json')
      response.end(JSON.stringify({ host: request.headers.host, cookie: request.headers.cookie, peer: request.socket.remoteAddress }))
    })
    try {
      server.listen(0, E2E_HUB_HOST)
      await once(server, 'listening')
      const address = server.address()
      if (!address || typeof address === 'string')
        throw new Error('The private HTTP server has no TCP address.')
      const response = await fetchHub(`http://localhost:${address.port}/`, { headers: { Cookie: 'private-session' } })
      expect(await response.json()).toEqual({ host: `127.0.0.1:${address.port}`, cookie: 'private-session', peer: E2E_HUB_HOST })
    }
    finally {
      server.closeAllConnections()
      if (server.listening)
        await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()))
    }
  })

  it('keeps caller options and headers intact', async () => {
    const transport = vi.fn(async (_url: string, _init?: RequestInit) => new Response())
    vi.stubGlobal('fetch', transport)
    const headers = new Headers({ Cookie: 'private-session' })
    const controller = new AbortController()
    const options: RequestInit = { method: 'POST', body: '0', redirect: 'manual', signal: controller.signal, headers }
    await fetchHub('http://localhost:43210/path', options)
    const [url, sent] = transport.mock.calls[0]!
    expect(url).toBe('http://127.0.0.1:43210/path')
    expect(sent).toMatchObject({ method: 'POST', body: '0', redirect: 'manual', signal: controller.signal })
    expect(sent).toBe(options)
    expect([...headers]).toEqual([['cookie', 'private-session']])
  })

  it('passes an unrelated request and its options through unchanged', async () => {
    const transport = vi.fn(async () => new Response())
    vi.stubGlobal('fetch', transport)
    const options = { method: 'POST', body: '' }
    await fetchHub('https://remote.example/path', options)
    expect(transport).toHaveBeenCalledExactlyOnceWith('https://remote.example/path', options)
  })

  it('propagates the original transport failure without retrying', async () => {
    const failure = new Error('The private listener refused the request.')
    const transport = vi.fn(async () => {
      throw failure
    })
    vi.stubGlobal('fetch', transport)
    await expect(fetchHub('http://localhost:43210/path')).rejects.toBe(failure)
    expect(transport).toHaveBeenCalledOnce()
  })
})

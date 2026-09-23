import type { AddressInfo, Socket } from 'node:net'
import { Buffer } from 'node:buffer'
import { createServer } from 'node:http'
import { connect as connectHttp2, createServer as createHttp2Server } from 'node:http2'
import { connect as netConnect } from 'node:net'
import { afterEach, describe, expect, it } from 'vitest'
import { createDualVersionListener } from './dualVersionListener'

const close: Array<() => Promise<void>> = []

afterEach(async () => {
  for (const stop of close.splice(0))
    await stop()
})

/** A listener whose handler remembers every path it served, in order. */
async function listen(): Promise<{ url: string, seen: string[] }> {
  const seen: string[] = []
  const handle = (request: { url?: string }, response: { end: (body: string) => void }): void => {
    seen.push(request.url ?? '')
    response.end(JSON.stringify({ path: request.url, count: seen.length }))
  }
  const http1 = createServer(handle as never)
  const http2 = createHttp2Server(handle as never)
  const { server } = createDualVersionListener(http1, http2)
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', () => resolve()))
  const { port } = server.address() as AddressInfo
  close.push(() => new Promise<void>((resolve) => {
    server.close(() => resolve())
    http1.closeAllConnections()
  }))
  return { url: `http://127.0.0.1:${port}`, seen }
}

describe('createDualVersionListener', () => {
  it('serves an HTTP/1.1 request', async () => {
    const { url } = await listen()
    const body = await (await fetch(`${url}/first`)).json() as { path: string }
    expect(body.path).toBe('/first')
  })

  // The mock endpoint registers a scenario, then reads it back, then serves a
  // provider's turns -- all on one listener. A router that only survives its
  // FIRST connection loses the registration, and the read answers 404 for a
  // scenario that was created successfully moments earlier.
  it('serves request after request, across separate connections', async () => {
    const { url, seen } = await listen()
    for (const path of ['/one', '/two', '/three'])
      expect((await (await fetch(`${url}${path}`)).json() as { path: string }).path).toBe(path)
    expect(seen).toEqual(['/one', '/two', '/three'])
  })

  it('serves concurrent requests', async () => {
    const { url } = await listen()
    const paths = ['/a', '/b', '/c', '/d']
    const bodies = await Promise.all(paths.map(async path =>
      (await (await fetch(`${url}${path}`)).json() as { path: string }).path))
    expect([...bodies].sort()).toEqual([...paths].sort())
  })

  it('serves a request whose body arrives after its headers', async () => {
    const { url } = await listen()
    const body = await (await fetch(`${url}/with-body`, { method: 'POST', body: 'x'.repeat(50_000) })).json() as { path: string }
    expect(body.path).toBe('/with-body')
  })
})

/**
 * A socket that writes its FIRST write one byte at a time, over several ticks.
 *
 * The HTTP/2 preface usually arrives whole, so a listener that compares only
 * the first chunk passes by luck. This forces the split the network is free to
 * produce at any time.
 */
function splittingConnection(port: number): Socket {
  const socket = netConnect(port, '127.0.0.1')
  const write = socket.write.bind(socket)
  let split = false
  socket.write = ((data: unknown, encoding?: unknown, callback?: unknown): boolean => {
    if (split || !Buffer.isBuffer(data))
      return (write as (...args: never[]) => boolean)(data as never, encoding as never, callback as never)
    split = true
    write(data.subarray(0, 1))
    let at = 1
    const rest = (): void => {
      if (at >= data.byteLength) {
        if (typeof callback === 'function')
          (callback as () => void)()
        return
      }
      write(data.subarray(at, at + 1))
      at += 1
      setTimeout(rest, 1)
    }
    setTimeout(rest, 1)
    return true
  }) as Socket['write']
  return socket
}

/** One HTTP/2 request over this connection, answered as text. */
async function requestOverHttp2(url: string, path: string, createConnection?: () => Socket): Promise<string> {
  const client = connectHttp2(url, createConnection ? { createConnection } : {})
  try {
    return await new Promise<string>((resolve, reject) => {
      client.on('error', reject)
      const stream = client.request({ ':path': path })
      let body = ''
      stream.setEncoding('utf8')
      stream.on('data', chunk => (body += chunk))
      stream.on('end', () => resolve(body))
      stream.on('error', reject)
    })
  }
  finally {
    client.close()
  }
}

describe('createDualVersionListener over HTTP/2', () => {
  // The whole reason this listener exists. Until this case was covered, the
  // socket reached the HTTP/2 server with its preface already drained, and the
  // session failed with `Received bad client magic byte string` -- which an
  // agent reports only as "Session closed with error code 2".
  it('serves an HTTP/2 request on the same port', async () => {
    const { url, seen } = await listen()
    const body = await requestOverHttp2(url, '/h2')
    expect(JSON.parse(body)).toMatchObject({ path: '/h2' })
    expect(seen).toEqual(['/h2'])
  })

  it('serves an HTTP/2 request whose preface arrives one byte at a time', async () => {
    const { url, seen } = await listen()
    const { port } = new URL(url)
    const body = await requestOverHttp2(url, '/split', () => splittingConnection(Number(port)))
    expect(JSON.parse(body)).toMatchObject({ path: '/split' })
    expect(seen).toEqual(['/split'])
  })

  it('still serves HTTP/1.1 on the same port after an HTTP/2 connection', async () => {
    const { url, seen } = await listen()
    await requestOverHttp2(url, '/first')
    const response = await fetch(`${url}/second`)
    expect(await response.json()).toMatchObject({ path: '/second' })
    expect(seen).toEqual(['/first', '/second'])
  })
})

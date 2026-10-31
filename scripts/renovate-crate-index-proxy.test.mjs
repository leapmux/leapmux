import { afterEach, describe, expect, it } from 'bun:test'
import { agedIndexArgs, filterIndexFile, serveAgedIndex, startAgedIndex } from './renovate-crate-index-proxy.mjs'

const cutoffMs = Date.parse('2026-10-28T00:00:00Z')
const old = '2026-10-20T00:00:00Z'
const young = '2026-10-30T00:00:00Z'
function line(name, vers, pubtime) {
  return JSON.stringify({
    name,
    vers,
    deps: [],
    cksum: '0'.repeat(64),
    features: {},
    yanked: false,
    ...(pubtime === undefined
      ? {}
      : { pubtime }),
  })
}

const servers = []
afterEach(() => {
  for (const server of servers.splice(0)) server.stop(true)
})

// A sparse index that answers each crate file from `files`, and records each path it serves.
function upstreamIndex(files) {
  const paths = []
  const server = Bun.serve({
    hostname: '127.0.0.1',
    port: 0,
    fetch(request) {
      const { pathname } = new URL(request.url)
      paths.push(pathname)
      if (pathname === '/index/config.json')
        return Response.json({ dl: 'http://127.0.0.1:1/dl', api: 'http://127.0.0.1:1' })
      const text = files[pathname.replace('/index', '')]
      return text === undefined
        ? new Response('not found', { status: 404 })
        : new Response(text)
    },
  })
  servers.push(server)
  return { url: `http://127.0.0.1:${server.port}/index`, paths }
}

describe('agedIndexArgs', () => {
  it('replaces the crates.io source with the aged index for one cargo call', () => {
    expect(agedIndexArgs('sparse+http://127.0.0.1:4000/')).toEqual([
      '--config',
      'source.crates-io.replace-with="leapmux-aged-index"',
      '--config',
      'source.leapmux-aged-index.registry="sparse+http://127.0.0.1:4000/"',
    ])
  })
})

describe('filterIndexFile', () => {
  it('keeps each version published by the cutoff and each allowed version, verbatim and in order', () => {
    const text = [
      line('foo', '1.0.0', old),
      line('foo', '1.1.0', young),
      line('foo', '1.2.0', young),
      line('foo', '1.3.0'),
      line('foo', '1.4.0', '2026-10-28T00:00:00Z'),
    ].join('\n')
    expect(filterIndexFile(`${text}\n`, {
      cutoffMs,
      allowed: new Set(['foo@1.2.0']),
    })).toBe(`${[
      line('foo', '1.0.0', old),
      line('foo', '1.2.0', young),
      line('foo', '1.4.0', '2026-10-28T00:00:00Z'),
    ].join('\n')}\n`)
  })
  it('treats a version without a publication time as young unless it is allowed', () => {
    expect(filterIndexFile(line('foo', '1.3.0'), {
      cutoffMs,
      allowed: new Set(),
    })).toBe('')
    expect(filterIndexFile(line('foo', '1.3.0'), {
      cutoffMs,
      allowed: new Set(['foo@1.3.0']),
    })).toBe(`${line('foo', '1.3.0')}\n`)
  })
  it('reads an empty file as no versions, and fails on a line that is not an index entry', () => {
    expect(filterIndexFile('', {
      cutoffMs,
      allowed: new Set(),
    })).toBe('')
    expect(filterIndexFile('\n\n', {
      cutoffMs,
      allowed: new Set(),
    })).toBe('')
    expect(() => filterIndexFile('{"name":', {
      cutoffMs,
      allowed: new Set(),
    })).toThrow(SyntaxError)
    for (const entry of ['null', '{"name":"foo"}', '{"vers":"1.0.0"}']) {
      expect(() => filterIndexFile(entry, {
        cutoffMs,
        allowed: new Set(),
      }), entry).toThrow('must give the name and the version')
    }
  })
})

describe('serveAgedIndex', () => {
  const serve = (upstream, allowed = []) => {
    const server = serveAgedIndex({
      upstream,
      cutoffMs,
      allowed: new Set(allowed),
    })
    servers.push(server)
    return (path, init) => fetch(`http://127.0.0.1:${server.port}${path}`, init)
  }
  it('serves each crate file without its young versions, and the configuration unchanged', async () => {
    const upstream = upstreamIndex({
      '/3/f/foo': [
        line('foo', '1.0.0', old),
        line('foo', '1.1.0', young),
      ].join('\n'),
      '/se/rd/serde': line('serde', '1.0.0', young),
    })
    const get = serve(upstream.url, ['serde@1.0.0'])
    const foo = await get('/3/f/foo')
    expect(foo.status).toBe(200)
    expect(foo.headers.get('etag')).toBeNull()
    expect(await foo.text()).toBe(`${line('foo', '1.0.0', old)}\n`)
    expect(await (await get('/se/rd/serde')).text()).toBe(`${line('serde', '1.0.0', young)}\n`)
    expect(await (await get('/config.json')).json()).toEqual({ dl: 'http://127.0.0.1:1/dl', api: 'http://127.0.0.1:1' })
    expect(upstream.paths).toEqual(['/index/3/f/foo', '/index/se/rd/serde', '/index/config.json'])
  })
  it('keeps a missing crate missing, and refuses any path that is not an index file without an upstream read', async () => {
    const upstream = upstreamIndex({})
    const get = serve(upstream.url)
    expect((await get('/mi/ss/missing')).status).toBe(404)
    expect(upstream.paths).toEqual(['/index/mi/ss/missing'])
    for (const path of [
      '/',
      '/../secret',
      '/3/f/../../etc',
      '/Se/rd/Serde',
      '/ab/cd',
      '/1/ab',
      '/api/v1/crates/foo',
    ])
      expect((await get(path)).status, path).toBe(404)
    for (const method of ['POST', 'PUT', 'DELETE'])
      expect((await get('/3/f/foo', { method })).status, method).toBe(404)
    expect(upstream.paths).toEqual(['/index/mi/ss/missing'])
  })
  it('answers 502 for a failed upstream read and an invalid file, so cargo fails instead of resolving without it', async () => {
    const failing = serveAgedIndex({
      upstream: 'http://upstream.invalid',
      cutoffMs,
      allowed: new Set(),
      request: async url => url.endsWith('/3/b/bad')
        ? new Response('{"name":')
        : url.endsWith('/3/e/err')
          ? new Response('', { status: 500 })
          : Promise.reject(new TypeError('fetch failed')),
    })
    servers.push(failing)
    const get = path => fetch(`http://127.0.0.1:${failing.port}${path}`)
    for (const path of ['/3/b/bad', '/3/e/err', '/3/n/net']) {
      const response = await get(path)
      expect(response.status, path).toBe(502)
    }
  })
})

describe('startAgedIndex', () => {
  it('serves the aged index from a child process until it closes', async () => {
    const upstream = upstreamIndex({
      '/3/f/foo': [
        line('foo', '1.0.0', old),
        line('foo', '1.1.0', young),
        line('foo', '1.2.0', young),
      ].join('\n'),
    })
    const index = await startAgedIndex({
      upstream: upstream.url,
      cutoffMs,
      allowed: new Set(['foo@1.2.0']),
    })
    const port = /^sparse\+http:\/\/127\.0\.0\.1:(\d+)\/$/.exec(index.url)?.[1]
    expect(port).toBeDefined()
    try {
      expect(await (await fetch(`http://127.0.0.1:${port}/3/f/foo`)).text()).toBe(`${[
        line('foo', '1.0.0', old),
        line('foo', '1.2.0', young),
      ].join('\n')}\n`)
    }
    finally {
      await index.close()
    }
    await expect(fetch(`http://127.0.0.1:${port}/3/f/foo`)).rejects.toThrow()
    // A second close has nothing left to stop.
    await index.close()
  })
})

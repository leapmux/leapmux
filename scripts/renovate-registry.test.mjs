import { describe, expect, it, spyOn } from 'bun:test'
import { crateIndexUrl, createRegistryFetch, readCrateReleases, readNpmReleases, readRegistryJson, serialThrottle } from './renovate-registry.mjs'

describe('readRegistryJson', () => {
  it('sends the maintenance identity and a 30-second timeout, and returns the JSON body', async () => {
    const timeout = spyOn(AbortSignal, 'timeout')
    try {
      let seen
      const result = await readRegistryJson('https://registry.example/x', {
        request: async (url, init) => {
          seen = { url, agent: init.headers['User-Agent'], signal: init.signal === timeout.mock.results[0]?.value }
          return Response.json({ ok: true })
        },
      })
      expect(result).toEqual({ ok: true })
      expect(seen).toEqual({ url: 'https://registry.example/x', agent: 'leapmux-dependency-maintenance', signal: true })
      expect(timeout.mock.calls).toEqual([
        [30000],
      ])
    }
    finally {
      timeout.mockRestore()
    }
  })
  it('returns the caller value for a 404 answer only when the caller supplies one', async () => {
    const missing = async () => new Response('', { status: 404 })
    expect(await readRegistryJson('https://registry.example/x', { request: missing, notFound: null })).toBeNull()
    await expect(readRegistryJson('https://registry.example/x', { request: missing })).rejects.toThrow('failed: 404')
    await expect(readRegistryJson('https://registry.example/x', {
      request: async () => new Response('', { status: 500 }),
      notFound: null,
    })).rejects.toThrow('failed: 500')
  })
  it('fails on a response that is not successful', async () => {
    await expect(readRegistryJson('https://registry.example/x', {
      request: async () => new Response('', { status: 404 }),
      description: 'Fixture read',
    })).rejects.toThrow('Fixture read failed: 404 https://registry.example/x')
  })
})

describe('serialThrottle', () => {
  it('spaces calls in order and continues after a failed call', async () => {
    let clock = 0
    const sleeps = []
    const throttle = serialThrottle(1000, {
      now: () => clock,
      sleep: async (ms) => {
        sleeps.push(ms)
        clock += ms
      },
    })
    const order = []
    const results = await Promise.allSettled([
      throttle(async () => order.push(['a', clock])),
      throttle(async () => {
        order.push(['b', clock])
        throw new Error('failed call')
      }),
      throttle(async () => order.push(['c', clock])),
    ])
    expect(results.map(result => result.status)).toEqual(['fulfilled', 'rejected', 'fulfilled'])
    expect(order).toEqual([
      ['a', 0],
      ['b', 1000],
      ['c', 2000],
    ])
    expect(sleeps).toEqual([1000, 1000])
    expect(() => serialThrottle(-1)).toThrow('nonnegative')
  })
})

describe('createRegistryFetch', () => {
  const fixture = ({ answers, intervals = {}, retries } = {}) => {
    let clock = 0
    const sleeps = []
    const sent = []
    const queue = [...answers]
    const registryFetch = createRegistryFetch({
      fetch: async (url) => {
        sent.push([url, clock])
        const answer = queue.shift()
        if (answer instanceof Error)
          throw answer
        return answer
      },
      intervals,
      ...(retries === undefined
        ? {}
        : { retries }),
      now: () => clock,
      sleep: async (ms) => {
        sleeps.push(ms)
        clock += ms
      },
    })
    return { registryFetch, sleeps, sent }
  }
  it('retries a transient status and a network failure, then returns the answer', async () => {
    const { registryFetch, sleeps, sent } = fixture({
      answers: [
        new Response('', { status: 503 }),
        new TypeError('fetch failed'),
        Response.json({ ok: true }),
      ],
    })
    const response = await registryFetch('https://registry.example/x', {})
    expect(await response.json()).toEqual({ ok: true })
    expect(sent.map(([url]) => url)).toEqual(Array.from({ length: 3 }).fill('https://registry.example/x'))
    expect(sleeps).toEqual([1000, 2000])
  })
  it('waits the Retry-After delay up to ten seconds, and returns the last transient answer', async () => {
    const { registryFetch, sleeps } = fixture({
      answers: [
        new Response('', {
          status: 429,
          headers: { 'Retry-After': '3' },
        }),
        new Response('', {
          status: 429,
          headers: { 'Retry-After': '120' },
        }),
        new Response('', { status: 429 }),
      ],
    })
    expect((await registryFetch('https://registry.example/x', {})).status).toBe(429)
    expect(sleeps).toEqual([3000, 10000])
  })
  it('never retries a final answer, an aborted read, or a failure after the last retry', async () => {
    const notFound = fixture({
      answers: [
        new Response('', { status: 404 }),
      ],
    })
    expect((await notFound.registryFetch('https://registry.example/x', {})).status).toBe(404)
    expect(notFound.sleeps).toEqual([])
    const aborted = fixture({
      answers: [
        new Response('', { status: 503 }),
      ],
    })
    expect((await aborted.registryFetch('https://registry.example/x', {
      signal: AbortSignal.abort(),
    })).status).toBe(503)
    expect(aborted.sleeps).toEqual([])
    const failed = fixture({
      answers: [
        new TypeError('first'),
        new TypeError('last'),
      ],
      retries: 1,
    })
    await expect(failed.registryFetch('https://registry.example/x', {})).rejects.toThrow('last')
    expect(() => createRegistryFetch({ retries: -1 })).toThrow('nonnegative integer')
  })
  it('spaces the requests to a throttled host and leaves another host unspaced', async () => {
    const { registryFetch, sent } = fixture({
      answers: Array.from({ length: 4 }, () => Response.json({})),
      intervals: { 'crates.io': 1000 },
    })
    await Promise.all([
      registryFetch('https://crates.io/api/v1/crates/a', {}),
      registryFetch('https://registry.npmjs.org/b', {}),
      registryFetch('https://crates.io/api/v1/crates/c', {}),
      registryFetch('https://registry.npmjs.org/d', {}),
    ])
    expect(sent.filter(([url]) => url.includes('crates.io')).map(([, time]) => time)).toEqual([0, 1000])
    expect(sent.filter(([url]) => url.includes('npmjs')).map(([, time]) => time)).toEqual([0, 0])
  })
})

describe('crateIndexUrl', () => {
  it('files each crate under the directory of its lowercase name length and prefix', () => {
    expect([
      'a',
      'ab',
      'foo',
      'serde',
      'Serde_JSON',
    ].map(name => crateIndexUrl(name))).toEqual([
      'https://index.crates.io/1/a',
      'https://index.crates.io/2/ab',
      'https://index.crates.io/3/f/foo',
      'https://index.crates.io/se/rd/serde',
      'https://index.crates.io/se/rd/serde_json',
    ])
    expect(crateIndexUrl('tauri', 'http://127.0.0.1:1/index')).toBe('http://127.0.0.1:1/index/ta/ur/tauri')
  })
})

describe('readCrateReleases', () => {
  const answer = text => async () => new Response(text)
  it('reads each index line as a release, with its publication time and its yanked flag', async () => {
    const urls = []
    const lines = [
      JSON.stringify({
        name: 'foo',
        vers: '1.0.0',
        yanked: false,
        pubtime: '2026-10-20T00:00:00Z',
      }),
      '',
      JSON.stringify({ name: 'foo', vers: '1.1.0', yanked: true }),
      JSON.stringify({
        name: 'foo',
        vers: '1.2.0',
        yanked: false,
        pubtime: 7,
      }),
    ]
    expect(await readCrateReleases('foo', {
      request: async (url) => {
        urls.push(url)
        return new Response(`${lines.join('\n')}\n`)
      },
    })).toEqual([
      { version: '1.0.0', publishedAt: '2026-10-20T00:00:00Z', yanked: false },
      { version: '1.1.0', publishedAt: null, yanked: true },
      { version: '1.2.0', publishedAt: null, yanked: false },
    ])
    expect(urls).toEqual(['https://index.crates.io/3/f/foo'])
  })
  it('reads a crate that the index lacks, and an empty index file, as no releases', async () => {
    expect(await readCrateReleases('missing', {
      request: async () => new Response('', { status: 404 }),
    })).toEqual([])
    expect(await readCrateReleases('empty', {
      request: answer('\n'),
    })).toEqual([])
  })
  it('fails on a failed read, a line that is not JSON, and an entry without a version', async () => {
    await expect(readCrateReleases('foo', {
      request: async () => new Response('', { status: 500 }),
    })).rejects.toThrow('Crate index read failed: 500 https://index.crates.io/3/f/foo')
    await expect(readCrateReleases('foo', {
      request: answer('{"vers":'),
    })).rejects.toThrow(SyntaxError)
    for (const line of ['null', '{"name":"foo"}', '{"vers":1}']) {
      await expect(readCrateReleases('foo', {
        request: answer(line),
      }), line).rejects.toThrow('Invalid crate index entry for foo.')
    }
  })
})

describe('readNpmReleases', () => {
  it('reads the time of each version from the packument, without its creation and modification times', async () => {
    const urls = []
    expect(await readNpmReleases('@scope/pkg', {
      request: async (url) => {
        urls.push(url)
        return Response.json({
          time: {
            'created': '2026-01-01T00:00:00Z',
            '1.0.0': '2026-01-01T00:00:00Z',
            '1.1.0': '2026-10-20T00:00:00Z',
            'modified': '2026-10-20T00:00:00Z',
          },
        })
      },
    })).toEqual([
      { version: '1.0.0', publishedAt: '2026-01-01T00:00:00Z' },
      { version: '1.1.0', publishedAt: '2026-10-20T00:00:00Z' },
    ])
    expect(urls).toEqual(['https://registry.npmjs.org/%40scope%2Fpkg'])
  })
  it('fails on a failed read, including a package that the registry lacks', async () => {
    for (const status of [404, 500]) {
      await expect(readNpmReleases('pkg', {
        request: async () => new Response('', { status }),
      }), String(status)).rejects.toThrow(`npm packument read failed: ${status}`)
    }
  })
  it('fails on a packument without a time map', async () => {
    for (const body of [
      {},
      { time: 'yesterday' },
      { time: null },
      null,
    ]) {
      await expect(readNpmReleases('pkg', {
        request: async () => Response.json(body),
      }), JSON.stringify(body)).rejects.toThrow('Invalid npm packument: pkg')
    }
  })
})

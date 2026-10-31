import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it, spyOn } from 'bun:test'
import { publishTimeCutoff, readRegistryJson, releaseAgeMs, serialThrottle } from './renovate-release-age.mjs'

const root = resolve(import.meta.dirname, '..')

describe('releaseAgeMs', () => {
  it('computes the age as Renovate does, including a buffer', () => {
    expect(releaseAgeMs({ minimumReleaseAge: '4 days' })).toBe(345600000)
    expect(releaseAgeMs({ minimumReleaseAge: '4 days', minimumReleaseAgeBuffer: null })).toBe(345600000)
    expect(releaseAgeMs({ minimumReleaseAge: '4 days', minimumReleaseAgeBuffer: '30 minutes' })).toBe(345600000 + 1800000)
  })
  it('rejects a missing, zero, or unparsable age', () => {
    for (const minimumReleaseAge of ['0 days', '', 'soon', null, undefined])
      expect(() => releaseAgeMs({ minimumReleaseAge }), String(minimumReleaseAge)).toThrow('positive duration')
  })
  it('reads the repository policy as three days', () => {
    expect(releaseAgeMs(JSON.parse(readFileSync(resolve(root, 'renovate.json'), 'utf8')))).toBe(3 * 86400000)
  })
})

describe('publishTimeCutoff', () => {
  it('subtracts the age from the current time', () => {
    expect(publishTimeCutoff(259200000, Date.parse('2026-10-31T22:20:38Z')).toISOString()).toBe('2026-10-28T22:20:38.000Z')
  })
  it('rejects a nonpositive age and an invalid time', () => {
    expect(() => publishTimeCutoff(0, Date.now())).toThrow('positive release age')
    expect(() => publishTimeCutoff(1000, Number.NaN)).toThrow('finite time')
  })
})

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

describe('the release-age policy under Node', () => {
  it('computes the same age', () => {
    const source = `const {releaseAgeMs}=await import(${JSON.stringify(new URL('./renovate-release-age.mjs', import.meta.url).href)}); console.log(releaseAgeMs({minimumReleaseAge:'4 days',minimumReleaseAgeBuffer:'30 minutes'}))`
    const result = spawnSync('node', ['--input-type=module', '-e', source], { encoding: 'utf8' })
    expect(result.status, result.stderr).toBe(0)
    expect(Number(result.stdout)).toBe(345600000 + 1800000)
  })
})

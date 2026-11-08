import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, expect, it, spyOn } from 'bun:test'
import { downloadReleaseAsset, parseRetryAfter, retryBudgetMs, retryDelay } from './download.mjs'
import { scratchDirectories } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)
const fixture = () => scratch.create('download')

describe('downloadReleaseAsset', () => {
  /** Run `body` with console.log captured, and return the captured lines. */
  async function captureLog(body) {
    const lines = []
    const spy = spyOn(console, 'log').mockImplementation(message => lines.push(String(message)))
    try {
      await body()
    }
    finally {
      spy.mockRestore()
    }
    return lines
  }

  it('writes the response body to the destination', async () => {
    const dir = fixture()
    const destination = join(dir, 'asset')
    await downloadReleaseAsset('https://example.invalid/asset', destination, {
      fetchImpl: async () => new Response('binary-bytes'),
    })
    expect(readFileSync(destination, 'utf8')).toBe('binary-bytes')
  })

  it('reports the URL and the status when the download fails', async () => {
    const dir = fixture()
    let calls = 0
    await expect(downloadReleaseAsset('https://example.invalid/asset', join(dir, 'asset'), {
      fetchImpl: async () => {
        calls++
        return new Response('missing', { status: 404 })
      },
      sleep: async () => {},
    })).rejects.toThrow('Downloading https://example.invalid/asset failed with HTTP 404.')
    expect(calls).toBe(1)
  })

  it('retries a transient gateway failure and writes the body', async () => {
    const dir = fixture()
    const destination = join(dir, 'asset')
    const responses = [
      new Response('gateway timeout', { status: 504 }),
      new Response('binary-bytes'),
    ]
    const delays = []
    const lines = await captureLog(() => downloadReleaseAsset('https://example.invalid/asset', destination, {
      fetchImpl: async () => responses.shift(),
      sleep: async ms => delays.push(ms),
      random: () => 0.5,
    }))
    expect(readFileSync(destination, 'utf8')).toBe('binary-bytes')
    expect(delays).toEqual([750])
    expect(lines.join('\n')).toContain('Retrying (1/10) in 750ms.')
  })

  it('retries a network failure and writes the body', async () => {
    const dir = fixture()
    const destination = join(dir, 'asset')
    let calls = 0
    await downloadReleaseAsset('https://example.invalid/asset', destination, {
      fetchImpl: async () => {
        calls++
        if (calls === 1)
          throw new Error('socket hang up')
        return new Response('binary-bytes')
      },
      sleep: async () => {},
    })
    expect(readFileSync(destination, 'utf8')).toBe('binary-bytes')
    expect(calls).toBe(2)
  })

  it('fails after the first try and ten retries when every one is transient', async () => {
    const dir = fixture()
    let calls = 0
    const lines = await captureLog(() => expect(downloadReleaseAsset('https://example.invalid/asset', join(dir, 'asset'), {
      fetchImpl: async () => {
        calls++
        return new Response('gateway timeout', { status: 504 })
      },
      sleep: async () => {},
    })).rejects.toThrow('Downloading https://example.invalid/asset failed with HTTP 504.'))
    expect(calls).toBe(11)
    expect(lines.join('\n')).toContain('Giving up after 10 retries.')
  })

  it('backs off exponentially with jitter and caps each backoff at one minute', async () => {
    const dir = fixture()
    const delays = []
    await expect(downloadReleaseAsset('https://example.invalid/asset', join(dir, 'asset'), {
      fetchImpl: async () => new Response('gateway timeout', { status: 504 }),
      sleep: async ms => delays.push(ms),
      random: () => 0.5,
    })).rejects.toThrow('failed with HTTP 504.')
    expect(delays).toEqual([
      750,
      1500,
      3000,
      6000,
      12000,
      24000,
      48000,
      60000,
      60000,
      60000,
    ])
  })

  it('jitters each backoff between half and all of the exponential delay', () => {
    expect(retryDelay(0, () => 0)).toBe(500)
    expect(retryDelay(0, () => 1)).toBe(1000)
    expect(retryDelay(3, () => 0)).toBe(4000)
    expect(retryDelay(3, () => 1)).toBe(8000)
  })

  it('retries at the delay that Retry-After asks for', async () => {
    const dir = fixture()
    const destination = join(dir, 'asset')
    const responses = [
      new Response('too many requests', {
        status: 429,
        headers: { 'retry-after': '5' },
      }),
      new Response('binary-bytes'),
    ]
    const delays = []
    await downloadReleaseAsset('https://example.invalid/asset', destination, {
      fetchImpl: async () => responses.shift(),
      sleep: async ms => delays.push(ms),
      random: () => 0.5,
    })
    expect(readFileSync(destination, 'utf8')).toBe('binary-bytes')
    expect(delays).toEqual([5000])
  })

  it('waits the full Retry-After even above the one-minute cap', async () => {
    const dir = fixture()
    const destination = join(dir, 'asset')
    const responses = [
      new Response('too many requests', {
        status: 429,
        headers: { 'retry-after': '120' },
      }),
      new Response('binary-bytes'),
    ]
    const delays = []
    await downloadReleaseAsset('https://example.invalid/asset', destination, {
      fetchImpl: async () => responses.shift(),
      sleep: async ms => delays.push(ms),
    })
    expect(readFileSync(destination, 'utf8')).toBe('binary-bytes')
    expect(delays).toEqual([120000])
  })

  it('gives up quickly when Retry-After lies beyond the retry budget', async () => {
    const dir = fixture()
    let calls = 0
    const lines = await captureLog(() => expect(downloadReleaseAsset('https://example.invalid/asset', join(dir, 'asset'), {
      fetchImpl: async () => {
        calls++
        return new Response('service unavailable', {
          status: 503,
          headers: { 'retry-after': '600' },
        })
      },
      sleep: async () => {},
    })).rejects.toThrow('failed with HTTP 503. The server asked to retry after 600s, beyond the 303s retry budget.'))
    expect(calls).toBe(1)
    expect(lines.join('\n')).toContain('Giving up: The server asked to retry after 600s, beyond the 303s retry budget.')
  })
})

describe('parseRetryAfter', () => {
  it('reads the delta seconds of a Retry-After header', () => {
    expect(parseRetryAfter('5')).toBe(5000)
    expect(parseRetryAfter(' 30 ')).toBe(30000)
    expect(parseRetryAfter('0')).toBe(0)
  })

  it('reads the HTTP date of a Retry-After header', () => {
    const now = Date.parse('2026-11-08T10:00:00Z')
    expect(parseRetryAfter('Sun, 08 Nov 2026 10:00:30 GMT', now)).toBe(30000)
    expect(parseRetryAfter('Sun, 08 Nov 2026 09:59:30 GMT', now)).toBe(0)
  })

  it('falls back to the backoff for an absent or malformed header', () => {
    expect(parseRetryAfter(null)).toBeNull()
    expect(parseRetryAfter('')).toBeNull()
    expect(parseRetryAfter('soon')).toBeNull()
    expect(parseRetryAfter('-1')).toBeNull()
    expect(parseRetryAfter('2026-11')).toBeNull()
  })
})

describe('retryBudgetMs', () => {
  it('sums the capped backoff maxima of the retry schedule', () => {
    expect(retryBudgetMs(1)).toBe(1000)
    expect(retryBudgetMs(10)).toBe(303000)
  })
})

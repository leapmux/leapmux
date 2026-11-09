import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { downloadFile, retryBudgetMs, retryDelay } from './download.mjs'
import { scratchDirectories } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)
const fixture = () => scratch.create('download')

const URL_UNDER_TEST = 'https://example.invalid/asset'

describe('downloadFile', () => {
  /** A fetch that answers with the given responses in order, and counts its calls. */
  function scriptedFetch(...responses) {
    const fetched = { calls: 0 }
    fetched.fetchImpl = async () => {
      fetched.calls++
      const next = responses.length > 1
        ? responses.shift()
        : responses[0]
      return typeof next === 'function'
        ? next()
        : next.clone()
    }
    return fetched
  }

  /** The injected side channels: the recorded waits and log lines. */
  function recorder() {
    const recorded = { delays: [], lines: [] }
    recorded.sleep = async ms => recorded.delays.push(ms)
    recorded.log = line => recorded.lines.push(line)
    return recorded
  }

  it('writes the response body to the destination', async () => {
    const destination = join(fixture(), 'asset')
    await downloadFile(URL_UNDER_TEST, destination, {
      fetchImpl: async () => new Response('binary-bytes'),
    })
    expect(readFileSync(destination, 'utf8')).toBe('binary-bytes')
  })

  it('leaves no temporary file beside a completed download', async () => {
    const dir = fixture()
    writeFileSync(join(dir, 'asset.partial'), 'stale partial bytes')
    await downloadFile(URL_UNDER_TEST, join(dir, 'asset'), {
      fetchImpl: async () => new Response('binary-bytes'),
    })
    expect(readdirSync(dir)).toEqual(['asset'])
  })

  it('reports the URL and the status when the download fails', async () => {
    const fetched = scriptedFetch(new Response('missing', { status: 404 }))
    const recorded = recorder()
    await expect(downloadFile(URL_UNDER_TEST, join(fixture(), 'asset'), {
      ...fetched,
      ...recorded,
    }))
      .rejects
      .toThrow(`Downloading ${URL_UNDER_TEST} failed with HTTP 404.`)
    expect(fetched.calls).toBe(1)
    expect(recorded.lines).toEqual([`Downloading ${URL_UNDER_TEST} failed with HTTP 404. Giving up: the failure is not transient.`])
  })

  it('fails at once when the destination cannot be written', async () => {
    const fetched = scriptedFetch(new Response('binary-bytes'))
    const recorded = recorder()
    await expect(downloadFile(URL_UNDER_TEST, join(fixture(), 'missing', 'asset'), {
      ...fetched,
      ...recorded,
    }))
      .rejects
      .toThrow('ENOENT')
    expect(fetched.calls).toBe(1)
    expect(recorded.delays).toEqual([])
  })

  it('fails at once and removes the temporary file when the rename fails', async () => {
    const dir = fixture()
    const destination = join(dir, 'asset')
    // A non-empty directory at the destination makes the rename fail after
    // the temporary file is complete.
    mkdirSync(join(destination, 'occupied'), { recursive: true })
    const fetched = scriptedFetch(new Response('binary-bytes'))
    await expect(downloadFile(URL_UNDER_TEST, destination, {
      ...fetched,
      ...recorder(),
    })).rejects.toThrow()
    expect(fetched.calls).toBe(1)
    expect(existsSync(`${destination}.partial`)).toBe(false)
  })

  it.skipIf(process.platform === 'win32')('leaves no truncated file at the destination when the write fails', () => {
    const destination = join(fixture(), 'asset')
    const script = `
      import { downloadFile } from ${JSON.stringify(new URL('./download.mjs', import.meta.url).href)}
      try {
        await downloadFile(${JSON.stringify(URL_UNDER_TEST)}, ${JSON.stringify(destination)}, {
          fetchImpl: async () => new Response(new Uint8Array(64 * 1024)),
          log: () => {},
        })
        console.log('downloaded')
      }
      catch (error) {
        console.log(error.code)
      }
    `
    // ulimit -f counts 512-byte blocks, so the write of the 64 KiB body
    // stops at 16 KiB with EFBIG.
    const result = spawnSync('bash', ['-c', 'ulimit -f 32; exec node --input-type=module -e "$0"', script], { encoding: 'utf8' })
    expect(result.status, result.stderr).toBe(0)
    expect(result.stdout.trim()).toBe('EFBIG')
    expect(existsSync(destination)).toBe(false)
    expect(existsSync(`${destination}.partial`)).toBe(false)
  })

  it('refuses a malformed URL without a request', async () => {
    const fetched = scriptedFetch(new Response('binary-bytes'))
    await expect(downloadFile('not a url', join(fixture(), 'asset'), {
      ...fetched,
      ...recorder(),
    }))
      .rejects
      .toThrow('The download URL not a url is malformed.')
    expect(fetched.calls).toBe(0)
  })

  it('retries a transient gateway failure and writes the body', async () => {
    const destination = join(fixture(), 'asset')
    const fetched = scriptedFetch(new Response('gateway timeout', { status: 504 }), new Response('binary-bytes'))
    const recorded = recorder()
    await downloadFile(URL_UNDER_TEST, destination, {
      ...fetched,
      ...recorded,
      random: () => 0.5,
    })
    expect(readFileSync(destination, 'utf8')).toBe('binary-bytes')
    expect(recorded.delays).toEqual([750])
    expect(recorded.lines).toEqual([`Downloading ${URL_UNDER_TEST} failed with HTTP 504. Retrying (1/10) in 750ms.`])
  })

  it('retries a network failure, states its cause, and writes the body', async () => {
    const destination = join(fixture(), 'asset')
    const refused = () => {
      throw new TypeError('fetch failed', {
        cause: Object.assign(new Error('connect ECONNREFUSED 127.0.0.1:443'), { code: 'ECONNREFUSED' }),
      })
    }
    const fetched = scriptedFetch(refused, new Response('binary-bytes'))
    const recorded = recorder()
    await downloadFile(URL_UNDER_TEST, destination, {
      ...fetched,
      ...recorded,
      random: () => 1 / 3,
    })
    expect(readFileSync(destination, 'utf8')).toBe('binary-bytes')
    expect(fetched.calls).toBe(2)
    expect(recorded.lines).toEqual([`Downloading ${URL_UNDER_TEST} failed: fetch failed (ECONNREFUSED). Retrying (1/10) in 667ms.`])
  })

  it('keeps the cause of a network failure on the final error', async () => {
    const cause = Object.assign(new Error('getaddrinfo ENOTFOUND example.invalid'), { code: 'ENOTFOUND' })
    const fetched = scriptedFetch(() => {
      throw new TypeError('fetch failed', { cause })
    })
    const failure = await downloadFile(URL_UNDER_TEST, join(fixture(), 'asset'), {
      ...fetched,
      ...recorder(),
      retries: 1,
    })
      .catch(error => error)
    expect(failure.message).toBe(`Downloading ${URL_UNDER_TEST} failed: fetch failed (ENOTFOUND).`)
    expect(failure.cause.cause).toBe(cause)
  })

  it('fails after the first try and ten retries when every one is transient', async () => {
    const fetched = scriptedFetch(new Response('gateway timeout', { status: 504 }))
    const recorded = recorder()
    await expect(downloadFile(URL_UNDER_TEST, join(fixture(), 'asset'), {
      ...fetched,
      ...recorded,
    }))
      .rejects
      .toThrow(`Downloading ${URL_UNDER_TEST} failed with HTTP 504.`)
    expect(fetched.calls).toBe(11)
    expect(recorded.lines.at(-1)).toBe(`Downloading ${URL_UNDER_TEST} failed with HTTP 504. Giving up after 10 retries.`)
  })

  it('names a single retry in the singular', async () => {
    const recorded = recorder()
    await expect(downloadFile(URL_UNDER_TEST, join(fixture(), 'asset'), {
      ...scriptedFetch(new Response('gateway timeout', { status: 504 })),
      ...recorded,
      retries: 1,
    })).rejects.toThrow('HTTP 504.')
    expect(recorded.lines.at(-1)).toEndWith('Giving up after 1 retry.')
  })

  it('backs off exponentially with jitter and caps each backoff ceiling at one minute', async () => {
    const recorded = recorder()
    await expect(downloadFile(URL_UNDER_TEST, join(fixture(), 'asset'), {
      ...scriptedFetch(new Response('gateway timeout', { status: 504 })),
      ...recorded,
      random: () => 0.5,
    })).rejects.toThrow('failed with HTTP 504.')
    expect(recorded.delays).toEqual([
      750,
      1500,
      3000,
      6000,
      12000,
      24000,
      45000,
      45000,
      45000,
      45000,
    ])
  })

  it('retries at the delay that Retry-After asks for', async () => {
    const destination = join(fixture(), 'asset')
    const recorded = recorder()
    await downloadFile(URL_UNDER_TEST, destination, {
      ...scriptedFetch(new Response('too many requests', {
        status: 429,
        headers: { 'retry-after': '5' },
      }), new Response('binary-bytes')),
      ...recorded,
      random: () => 0.5,
    })
    expect(readFileSync(destination, 'utf8')).toBe('binary-bytes')
    expect(recorded.delays).toEqual([5000])
  })

  it('waits the full Retry-After even above the one-minute cap', async () => {
    const destination = join(fixture(), 'asset')
    const recorded = recorder()
    await downloadFile(URL_UNDER_TEST, destination, {
      ...scriptedFetch(new Response('too many requests', {
        status: 429,
        headers: { 'retry-after': '120' },
      }), new Response('binary-bytes')),
      ...recorded,
    })
    expect(readFileSync(destination, 'utf8')).toBe('binary-bytes')
    expect(recorded.delays).toEqual([120000])
  })

  it('never retries sooner than the backoff when Retry-After asks for no wait', async () => {
    // A zero wait would spend every retry within a second and defeat the
    // backoff that outlasts an outage.
    const recorded = recorder()
    await expect(downloadFile(URL_UNDER_TEST, join(fixture(), 'asset'), {
      ...scriptedFetch(new Response('service unavailable', {
        status: 503,
        headers: { 'retry-after': '0' },
      })),
      ...recorded,
      random: () => 0.5,
    })).rejects.toThrow('failed with HTTP 503.')
    expect(recorded.delays).toEqual([
      750,
      1500,
      3000,
      6000,
      12000,
      24000,
      45000,
      45000,
      45000,
      45000,
    ])
  })

  it('never retries sooner than the backoff when the Retry-After date has passed', async () => {
    const clock = Date.parse('2026-11-08T10:00:00Z')
    const recorded = recorder()
    await downloadFile(URL_UNDER_TEST, join(fixture(), 'asset'), {
      ...scriptedFetch(new Response('service unavailable', {
        status: 503,
        headers: { 'retry-after': 'Sun, 08 Nov 2026 09:59:30 GMT' },
      }), new Response('binary-bytes')),
      ...recorded,
      now: () => clock,
      random: () => 0.5,
    })
    expect(recorded.delays).toEqual([750])
  })

  it('waits until the HTTP date that Retry-After gives', async () => {
    const clock = Date.parse('2026-11-08T10:00:00Z')
    const recorded = recorder()
    await downloadFile(URL_UNDER_TEST, join(fixture(), 'asset'), {
      ...scriptedFetch(new Response('service unavailable', {
        status: 503,
        headers: { 'retry-after': 'Sun, 08 Nov 2026 10:00:30 GMT' },
      }), new Response('binary-bytes')),
      ...recorded,
      now: () => clock,
    })
    expect(recorded.delays).toEqual([30000])
  })

  it('gives up quickly when Retry-After lies beyond the retry budget', async () => {
    const fetched = scriptedFetch(new Response('service unavailable', {
      status: 503,
      headers: { 'retry-after': '600' },
    }))
    const recorded = recorder()
    await expect(downloadFile(URL_UNDER_TEST, join(fixture(), 'asset'), {
      ...fetched,
      ...recorded,
    }))
      .rejects
      .toThrow('failed with HTTP 503. The server asked to retry after 600s, beyond the 303s left of the 303s retry budget.')
    expect(fetched.calls).toBe(1)
    expect(recorded.delays).toEqual([])
    expect(recorded.lines).toEqual([`Downloading ${URL_UNDER_TEST} failed with HTTP 503. Giving up: The server asked to retry after 600s, beyond the 303s left of the 303s retry budget.`])
  })

  it('gives up when an HTTP-date Retry-After lies beyond the retry budget', async () => {
    const clock = Date.parse('2026-11-08T10:00:00Z')
    const fetched = scriptedFetch(new Response('service unavailable', {
      status: 503,
      headers: { 'retry-after': 'Sun, 08 Nov 2026 10:10:00 GMT' },
    }))
    await expect(downloadFile(URL_UNDER_TEST, join(fixture(), 'asset'), {
      ...fetched,
      ...recorder(),
      now: () => clock,
    }))
      .rejects
      .toThrow('The server asked to retry after 600s')
    expect(fetched.calls).toBe(1)
  })

  it('deducts the waits already spent from the Retry-After budget', async () => {
    const fetched = scriptedFetch(new Response('too many requests', {
      status: 429,
      headers: { 'retry-after': '300' },
    }))
    const recorded = recorder()
    await expect(downloadFile(URL_UNDER_TEST, join(fixture(), 'asset'), {
      ...fetched,
      ...recorded,
    }))
      .rejects
      .toThrow('The server asked to retry after 300s, beyond the 3s left of the 303s retry budget.')
    expect(recorded.delays).toEqual([300000])
    expect(fetched.calls).toBe(2)
  })

  it('gives up when the next attempt would start after the download deadline', async () => {
    // Each attempt hangs until its five-minute timeout, as a CDN that stalls
    // every connection does.
    let clock = 0
    let calls = 0
    const recorded = recorder()
    const failure = await downloadFile(URL_UNDER_TEST, join(fixture(), 'asset'), {
      fetchImpl: async () => {
        calls++
        clock += 5 * 60 * 1000
        throw new DOMException('The operation was aborted due to timeout', 'TimeoutError')
      },
      sleep: async (ms) => {
        recorded.delays.push(ms)
        clock += ms
      },
      log: recorded.log,
      now: () => clock,
      random: () => 0.5,
    }).catch(error => error)
    expect(calls).toBe(3)
    expect(clock).toBeLessThanOrEqual(15 * 60 * 1000 + 5 * 60 * 1000)
    expect(failure.message).toBe(`Downloading ${URL_UNDER_TEST} failed: The operation was aborted due to timeout. The next attempt would start after the 900s download deadline.`)
    expect(recorded.lines.at(-1)).toEndWith('Giving up: The next attempt would start after the 900s download deadline.')
  })
})

describe('retryDelay', () => {
  it('jitters each backoff between half and all of its ceiling', () => {
    expect(retryDelay(0, () => 0)).toBe(500)
    expect(retryDelay(0, () => 1)).toBe(1000)
    expect(retryDelay(3, () => 0)).toBe(4000)
    expect(retryDelay(3, () => 1)).toBe(8000)
  })

  it('keeps the jitter of the retries at the one-minute cap', () => {
    expect(retryDelay(9, () => 0)).toBe(30000)
    expect(retryDelay(9, () => 0.5)).toBe(45000)
    expect(retryDelay(9, () => 1)).toBe(60000)
  })
})

describe('retryBudgetMs', () => {
  it('sums the capped backoff ceilings of the retry schedule', () => {
    expect(retryBudgetMs(0)).toBe(0)
    expect(retryBudgetMs(1)).toBe(1000)
    expect(retryBudgetMs(10)).toBe(303000)
  })
})

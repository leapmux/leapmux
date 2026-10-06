import { expect as playwrightExpect } from '@playwright/test'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { retryUntilPass } from './retryUntilPass'
import { startTestDeadline, startWaitLimitForTests, WAIT_REPORT_MARGIN_MS } from './testDeadline'

const ends: Array<() => void> = []

afterEach(() => {
  for (const end of ends.splice(0))
    end()
})

/** Limit the waits of one case to `limitMs`, so that a wait that never passes ends inside the unit test. */
function limitWaitTo(limitMs: number): void {
  ends.push(startWaitLimitForTests(limitMs))
}

describe('retryUntilPass', () => {
  it('returns the value of an attempt that passes at once', async () => {
    const attempt = vi.fn(async () => 'ready')
    await expect(retryUntilPass(attempt)).resolves.toBe('ready')
    expect(attempt).toHaveBeenCalledOnce()
  })

  it('runs the attempt again after a read that throws once, and returns the value of the next attempt', async () => {
    const read = vi.fn<() => Promise<string>>()
      .mockRejectedValueOnce(new Error('The Worker channel closed.'))
      .mockResolvedValue('active')
    await expect(retryUntilPass(read)).resolves.toBe('active')
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('runs the attempt again after a failed Playwright assertion', async () => {
    const values = ['starting', 'starting', 'active']
    let reads = 0
    const status = await retryUntilPass(() => {
      const value = values[Math.min(reads++, values.length - 1)]
      playwrightExpect(value).toBe('active')
      return value
    })
    expect(status).toBe('active')
    expect(reads).toBe(3)
  })

  it('runs a synchronous attempt that throws again', async () => {
    let reads = 0
    const value = await retryUntilPass(() => {
      if (reads++ === 0)
        throw new Error('The session file is half written.')
      return 'complete'
    })
    expect(value).toBe('complete')
  })

  it.each([undefined, null, 0, '', false])('returns the falsy value %j of the attempt that passed', async (value) => {
    await expect(retryUntilPass(() => value)).resolves.toBe(value)
  })

  it('states the error of the last attempt, and keeps it as the cause, when the wait reaches its limit', async () => {
    limitWaitTo(400)
    const errors: Error[] = []
    const failure = await retryUntilPass(async () => {
      const error = new Error(`The Worker read ${errors.length + 1} failed.`)
      errors.push(error)
      throw error
    }).catch((error: unknown) => error)
    if (!(failure instanceof Error))
      throw new Error('The wait did not fail.')
    const last = errors.at(-1)
    expect(failure.message.split('\n')[0]).toBe(last?.message)
    expect(failure.message).toContain('ended before the test\'s own deadline')
    expect(failure.cause).toBe(last)
  })

  it('ends an attempt that does not end before the limit, and states that no attempt ended', async () => {
    limitWaitTo(200)
    const failure = retryUntilPass(() => new Promise<never>(() => {}))
    await expect(failure).rejects.toThrow('No attempt ended before the limit of the wait.')
  })

  it('runs one attempt and stops when the test deadline already passed', async () => {
    ends.push(startTestDeadline(Date.now() - 60_000, () => WAIT_REPORT_MARGIN_MS))
    const attempt = vi.fn(async () => {
      throw new Error('The Worker read failed.')
    })
    await expect(retryUntilPass(attempt)).rejects.toThrow('The Worker read failed.')
    expect(attempt).toHaveBeenCalledOnce()
  })
})

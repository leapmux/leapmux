import type { Browser, Page, TestInfo } from '@playwright/test'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { withExtraClients } from './multiClient'
import { attachToastLog, installToastRecorder } from './toast'

const testInfo = vi.hoisted(() => ({ attach: vi.fn() }))
vi.mock('@playwright/test', () => ({ test: { info: () => testInfo } }))
vi.mock('./toast', () => ({
  attachToastLog: vi.fn(async () => {}),
  installToastRecorder: vi.fn(async () => {}),
}))

interface FakeContext {
  newPage: ReturnType<typeof vi.fn>
  close: ReturnType<typeof vi.fn>
  page: Page
}

/** A context whose `newPage` returns a distinct fake page, or fails with `failure`. */
function fakeContext(name: string, failure?: Error): FakeContext {
  const page = { name } as unknown as Page
  return {
    page,
    newPage: vi.fn(async () => {
      if (failure)
        throw failure
      return page
    }),
    close: vi.fn(async () => {}),
  }
}

/** A browser that hands out `contexts` in order and records the options of each `newContext` call. */
function fakeBrowser(contexts: FakeContext[]) {
  const queue = [...contexts]
  const newContext = vi.fn(async () => {
    const next = queue.shift()
    if (!next)
      throw new Error('The test opened more contexts than it prepared.')
    return next
  })
  return { browser: { newContext } as unknown as Browser, newContext }
}

const server = { hubUrl: 'http://127.0.0.1:4321' }

beforeEach(() => {
  vi.mocked(attachToastLog).mockReset().mockResolvedValue(undefined)
  vi.mocked(installToastRecorder).mockReset().mockResolvedValue(undefined)
})

describe('withExtraClients', () => {
  it('opens each client in its own context on the hub, records its toasts, and closes every context', async () => {
    const contexts = [fakeContext('a'), fakeContext('b')]
    const { browser, newContext } = fakeBrowser(contexts)
    const result = await withExtraClients(browser, server, 2, async ([pageA, pageB]) => {
      expect(pageA).toBe(contexts[0]!.page)
      expect(pageB).toBe(contexts[1]!.page)
      for (const context of contexts)
        expect(context.close).not.toHaveBeenCalled()
      return 'done'
    })
    expect(result).toBe('done')
    expect(newContext.mock.calls).toEqual([[{ baseURL: server.hubUrl }], [{ baseURL: server.hubUrl }]])
    expect(vi.mocked(installToastRecorder).mock.calls).toEqual([[contexts[0]!.page], [contexts[1]!.page]])
    expect(vi.mocked(attachToastLog).mock.calls).toEqual([
      [contexts[0]!.page, testInfo as unknown as TestInfo, 'toast-log-client-1'],
      [contexts[1]!.page, testInfo as unknown as TestInfo, 'toast-log-client-2'],
    ])
    for (const context of contexts)
      expect(context.close).toHaveBeenCalledTimes(1)
  })

  it('closes every context and rethrows the error of the test when the test fails', async () => {
    const contexts = [fakeContext('a'), fakeContext('b')]
    const { browser } = fakeBrowser(contexts)
    const failure = new Error('The assertion of the test failed.')
    await expect(withExtraClients(browser, server, 2, async () => {
      throw failure
    })).rejects.toBe(failure)
    for (const context of contexts)
      expect(context.close).toHaveBeenCalledTimes(1)
  })

  it('closes the context that it opened when the setup of a later client fails, and never calls the test', async () => {
    const failure = new Error('The controlled second page setup failed.')
    const contexts = [fakeContext('a'), fakeContext('b', failure)]
    const { browser } = fakeBrowser(contexts)
    const use = vi.fn(async () => {})
    await expect(withExtraClients(browser, server, 2, use)).rejects.toBe(failure)
    expect(use).not.toHaveBeenCalled()
    for (const context of contexts)
      expect(context.close).toHaveBeenCalledTimes(1)
    // The page that never opened has no toast log to attach.
    expect(vi.mocked(attachToastLog).mock.calls).toEqual([[contexts[0]!.page, testInfo as unknown as TestInfo, 'toast-log-client-1']])
  })

  it('closes the other contexts when one close fails, and reports the failed close', async () => {
    const contexts = [fakeContext('a'), fakeContext('b')]
    const closeFailure = new Error('The first context did not close.')
    contexts[0]!.close.mockRejectedValue(closeFailure)
    const { browser } = fakeBrowser(contexts)
    const error = await withExtraClients(browser, server, 2, async () => {}).catch((caught: unknown) => caught)
    expect(error).toBeInstanceOf(AggregateError)
    expect((error as AggregateError).errors).toEqual([closeFailure])
    expect(contexts[1]!.close).toHaveBeenCalledTimes(1)
  })

  it('closes a context although its toast log cannot attach', async () => {
    const contexts = [fakeContext('a')]
    const attachFailure = new Error('The report refused the attachment.')
    vi.mocked(attachToastLog).mockRejectedValue(attachFailure)
    const { browser } = fakeBrowser(contexts)
    const error = await withExtraClients(browser, server, 1, async () => {}).catch((caught: unknown) => caught)
    expect((error as AggregateError).errors).toEqual([attachFailure])
    expect(contexts[0]!.close).toHaveBeenCalledTimes(1)
  })

  it('refuses a client count outside one to three before it opens a context', async () => {
    const { browser, newContext } = fakeBrowser([])
    for (const count of [0, 4, 1.5, -1]) {
      await expect(withExtraClients(browser, server, count as 1, async () => {})).rejects.toThrow(RangeError)
    }
    expect(newContext).not.toHaveBeenCalled()
  })
})

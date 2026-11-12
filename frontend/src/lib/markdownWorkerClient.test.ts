import { afterEach, describe, expect, it, vi } from 'vitest'

const originalWorkerDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'Worker')

/** Any pair; these tests are about the transport, not about the colours. */
const PAIR = { light: 'github-light', dark: 'github-dark' }

function restoreWorker() {
  if (originalWorkerDescriptor) {
    Object.defineProperty(globalThis, 'Worker', originalWorkerDescriptor)
    return
  }

  Reflect.deleteProperty(globalThis, 'Worker')
}

async function importClient() {
  vi.resetModules()
  return await import('./markdownWorkerClient')
}

// Captured-worker/message lookup: each test pushes exactly the entries it
// expects, so a missing entry is a harness failure, not a case to guard.
function captured<T>(list: readonly T[], index: number): T {
  const value = list[index]
  if (value === undefined)
    throw new Error(`expected a captured entry at index ${index}`)
  return value
}

describe('markdownWorkerClient', () => {
  afterEach(() => {
    restoreWorker()
  })

  it('returns null when Web Workers are unavailable', async () => {
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: undefined,
    })
    const { renderMarkdownInWorker } = await importClient()

    await expect(renderMarkdownInWorker('no worker', PAIR)).resolves.toBeNull()
  })

  it('returns null when creating the worker throws synchronously', async () => {
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: class ThrowingWorker {
        constructor() {
          throw new Error('blocked by CSP')
        }
      },
    })
    const { renderMarkdownInWorker } = await importClient()

    await expect(renderMarkdownInWorker('no worker', PAIR)).resolves.toBeNull()
  })

  it('resolves all pending renders when postMessage throws synchronously', async () => {
    const terminateSpy = vi.fn()
    let posts = 0
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: class FlakyWorker {
        onmessage: ((event: MessageEvent) => void) | null = null
        onerror: (() => void) | null = null
        terminate = terminateSpy

        postMessage() {
          posts++
          if (posts === 2)
            throw new Error('worker port closed')
        }
      },
    })
    const { renderMarkdownInWorker } = await importClient()

    const first = renderMarkdownInWorker('first', PAIR)
    const second = renderMarkdownInWorker('second', PAIR)

    await expect(second).resolves.toBeNull()
    await expect(first).resolves.toBeNull()
    expect(terminateSpy).toHaveBeenCalledTimes(1)
  })

  it('ignores a stale error event from a replaced worker', async () => {
    const workers: Array<{
      onmessage: ((event: MessageEvent) => void) | null
      onerror: (() => void) | null
      messages: Array<{ id: number, text: string }>
      terminate: ReturnType<typeof vi.fn>
    }> = []
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: class CapturingWorker {
        onmessage: ((event: MessageEvent) => void) | null = null
        onerror: (() => void) | null = null
        messages: Array<{ id: number, text: string }> = []
        terminate = vi.fn()

        constructor() {
          workers.push(this)
        }

        postMessage(message: { id: number, text: string }) {
          this.messages.push(message)
        }
      },
    })
    const { renderMarkdownInWorker } = await importClient()

    const first = renderMarkdownInWorker('first', PAIR)
    captured(workers, 0).onerror?.()
    await expect(first).resolves.toBeNull()

    const second = renderMarkdownInWorker('second', PAIR)
    let settled = false
    second.then(() => {
      settled = true
    })
    captured(workers, 0).onerror?.()
    await Promise.resolve()
    expect(settled).toBe(false)

    const { id } = captured(captured(workers, 1).messages, 0)
    captured(workers, 1).onmessage?.({
      data: {
        id,
        html: '<p>second</p>',
        retryable: false,
        styles: { 'sk-x-1': '--shiki-light:#abc' },
      },
    } as MessageEvent)
    await expect(second).resolves.toEqual({
      html: '<p>second</p>',
      retryable: false,
      styles: { 'sk-x-1': '--shiki-light:#abc' },
    })
  })

  it('posts the pair it was given, not the one live when the gate releases the job', async () => {
    // The gate holds all but `maxInFlight` (2) jobs client-side, so the third
    // render's message is BUILT after an arbitrary delay. Reading the module
    // pair there sent the theme that was live at send time, while the caller
    // had already captured the old namespace to file the answer under -- so the
    // artifact was written under one theme carrying another theme's colours.
    const workers: Array<{
      onmessage: ((event: MessageEvent) => void) | null
      messages: Array<{ id: number, text: string, syntax: { light: string, dark: string } }>
    }> = []
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: class CapturingWorker {
        onmessage: ((event: MessageEvent) => void) | null = null
        onerror: (() => void) | null = null
        messages: Array<{ id: number, text: string, syntax: { light: string, dark: string } }> = []
        terminate = vi.fn()

        constructor() {
          workers.push(this)
        }

        postMessage(message: { id: number, text: string, syntax: { light: string, dark: string } }) {
          this.messages.push(message)
        }
      },
    })
    vi.resetModules()
    const { renderMarkdownInWorker } = await import('./markdownWorkerClient')
    const { setSyntaxThemePair } = await import('./shikiThemes')

    const oldPair = { light: 'github-light', dark: 'github-dark' }
    const newPair = { light: 'nord-light', dark: 'nord' }

    // Two jobs occupy the gate; the third is held.
    const held: Array<Promise<unknown>> = [
      renderMarkdownInWorker('a', oldPair),
      renderMarkdownInWorker('b', oldPair),
      renderMarkdownInWorker('c', oldPair),
    ]
    expect(captured(workers, 0).messages).toHaveLength(2)

    // The user picks another theme while 'c' is still queued.
    setSyntaxThemePair(newPair)

    // Release a slot so 'c' is posted.
    const first = captured(captured(workers, 0).messages, 0)
    captured(workers, 0).onmessage?.({
      data: {
        id: first.id,
        html: '<p>a</p>',
        retryable: false,
        styles: {},
      },
    } as MessageEvent)
    await held[0]

    const posted = captured(workers, 0).messages.find(m => m.text === 'c')
    expect(posted?.syntax).toEqual(oldPair)
  })

  describe('render time budget', () => {
    it('gives every render the base budget plus a share for each KB of source', async () => {
      const { MARKDOWN_RENDER_BASE_BUDGET_MS, MARKDOWN_RENDER_BUDGET_MS_PER_KB, markdownRenderBudgetMs } = await importClient()

      expect(markdownRenderBudgetMs('')).toBe(MARKDOWN_RENDER_BASE_BUDGET_MS)
      expect(markdownRenderBudgetMs('x')).toBe(MARKDOWN_RENDER_BASE_BUDGET_MS + MARKDOWN_RENDER_BUDGET_MS_PER_KB)
      expect(markdownRenderBudgetMs('x'.repeat(1024))).toBe(MARKDOWN_RENDER_BASE_BUDGET_MS + MARKDOWN_RENDER_BUDGET_MS_PER_KB)
      expect(markdownRenderBudgetMs('x'.repeat(1024 * 1024))).toBe(MARKDOWN_RENDER_BASE_BUDGET_MS + 1024 * MARKDOWN_RENDER_BUDGET_MS_PER_KB)
    })

    it('resolves a render that runs past its budget to null, and renders the body behind it on a fresh worker', async () => {
      vi.useFakeTimers()
      try {
        const workers: Array<{
          onmessage: ((event: MessageEvent) => void) | null
          messages: Array<{ id: number, text: string }>
          terminate: ReturnType<typeof vi.fn>
        }> = []
        Object.defineProperty(globalThis, 'Worker', {
          configurable: true,
          writable: true,
          value: class CapturingWorker {
            onmessage: ((event: MessageEvent) => void) | null = null
            onerror: (() => void) | null = null
            messages: Array<{ id: number, text: string }> = []
            terminate = vi.fn()

            constructor() {
              workers.push(this)
            }

            postMessage(message: { id: number, text: string }) {
              this.messages.push(message)
            }
          },
        })
        const { markdownRenderBudgetMs, renderMarkdownInWorker } = await importClient()
        const nested = '*a '.repeat(2_000)

        const stuck = renderMarkdownInWorker(nested, PAIR)
        const behind = renderMarkdownInWorker('behind', PAIR)
        vi.advanceTimersByTime(markdownRenderBudgetMs(nested))

        await expect(stuck).resolves.toBeNull()
        expect(captured(workers, 0).terminate).toHaveBeenCalledOnce()
        const fresh = captured(workers, 1)
        expect(fresh.messages.map(m => m.text)).toEqual(['behind'])
        fresh.onmessage?.({
          data: {
            id: captured(fresh.messages, 0).id,
            html: '<p>behind</p>',
            retryable: false,
            styles: {},
          },
        } as MessageEvent)
        await expect(behind).resolves.toEqual({ html: '<p>behind</p>', retryable: false, styles: {} })
      }
      finally {
        vi.useRealTimers()
      }
    })
  })

  describe('render lanes', () => {
    interface LaneWorker {
      onmessage: ((event: MessageEvent) => void) | null
      messages: Array<{ id: number, text: string }>
    }

    function installLaneWorkers(): LaneWorker[] {
      const workers: LaneWorker[] = []
      Object.defineProperty(globalThis, 'Worker', {
        configurable: true,
        writable: true,
        value: class CapturingWorker {
          onmessage: ((event: MessageEvent) => void) | null = null
          onerror: (() => void) | null = null
          messages: Array<{ id: number, text: string }> = []
          terminate = vi.fn()

          constructor() {
            workers.push(this)
          }

          postMessage(message: { id: number, text: string }) {
            this.messages.push(message)
          }
        },
      })
      return workers
    }

    it('renders a body at the size limit in the standard lane, and a larger one in a worker of its own', async () => {
      const workers = installLaneWorkers()
      const { LARGE_MARKDOWN_BODY_CHARS, renderMarkdownInWorker } = await importClient()
      const atLimit = 'a'.repeat(LARGE_MARKDOWN_BODY_CHARS)
      const overLimit = 'b'.repeat(LARGE_MARKDOWN_BODY_CHARS + 1)

      void renderMarkdownInWorker(atLimit, PAIR)
      expect(workers).toHaveLength(1)
      void renderMarkdownInWorker(overLimit, PAIR)

      expect(workers).toHaveLength(2)
      expect(captured(workers, 0).messages.map(m => m.text)).toEqual([atLimit])
      expect(captured(workers, 1).messages.map(m => m.text)).toEqual([overLimit])
    })

    it('spawns no large-lane worker until a large body arrives', async () => {
      const workers = installLaneWorkers()
      const { renderMarkdownInWorker } = await importClient()

      void renderMarkdownInWorker('small one', PAIR)
      void renderMarkdownInWorker('small two', PAIR)
      void renderMarkdownInWorker('small three', PAIR)

      expect(workers).toHaveLength(1)
    })

    it('posts a small body at once while large bodies fill their own lane', async () => {
      const workers = installLaneWorkers()
      const { LARGE_MARKDOWN_BODY_CHARS, renderMarkdownInWorker } = await importClient()
      const large = (n: number) => `${n}`.padEnd(LARGE_MARKDOWN_BODY_CHARS + 1, 'x')

      // More large bodies than the large lane's gate posts at once: the rest wait.
      let largeSettled = 0
      for (const n of [1, 2, 3]) {
        void renderMarkdownInWorker(large(n), PAIR).then(() => {
          largeSettled++
        })
      }
      const small = renderMarkdownInWorker('small body', PAIR)

      const largeWorker = captured(workers, 0)
      const smallWorker = captured(workers, 1)
      expect(largeWorker.messages).toHaveLength(2)
      expect(smallWorker.messages.map(m => m.text)).toEqual(['small body'])

      // The small body completes while every large body is still pending.
      const { id } = captured(smallWorker.messages, 0)
      smallWorker.onmessage?.({
        data: {
          id,
          html: '<p>small body</p>',
          retryable: false,
          styles: {},
        },
      } as MessageEvent)
      await expect(small).resolves.toEqual({ html: '<p>small body</p>', retryable: false, styles: {} })
      expect(largeSettled).toBe(0)
    })
  })

  it('defaults a missing styles dictionary to empty (older worker response shape)', async () => {
    const workers: Array<{
      onmessage: ((event: MessageEvent) => void) | null
      messages: Array<{ id: number, text: string }>
    }> = []
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: class CapturingWorker {
        onmessage: ((event: MessageEvent) => void) | null = null
        onerror: (() => void) | null = null
        messages: Array<{ id: number, text: string }> = []
        terminate = vi.fn()

        constructor() {
          workers.push(this)
        }

        postMessage(message: { id: number, text: string }) {
          this.messages.push(message)
        }
      },
    })
    const { renderMarkdownInWorker } = await importClient()

    const pending = renderMarkdownInWorker('text', PAIR)
    const { id } = captured(captured(workers, 0).messages, 0)
    captured(workers, 0).onmessage?.({
      data: { id, html: '<p>text</p>', retryable: false },
    } as MessageEvent)
    await expect(pending).resolves.toEqual({ html: '<p>text</p>', retryable: false, styles: {} })
  })
})

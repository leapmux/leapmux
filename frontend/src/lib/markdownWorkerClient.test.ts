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
    workers[0].onerror?.()
    await expect(first).resolves.toBeNull()

    const second = renderMarkdownInWorker('second', PAIR)
    let settled = false
    second.then(() => {
      settled = true
    })
    workers[0].onerror?.()
    await Promise.resolve()
    expect(settled).toBe(false)

    const { id } = workers[1].messages[0]
    workers[1].onmessage?.({ data: { id, html: '<p>second</p>', retryable: false, styles: { 'sk-x-1': '--shiki-light:#abc' } } } as MessageEvent)
    await expect(second).resolves.toEqual({ html: '<p>second</p>', retryable: false, styles: { 'sk-x-1': '--shiki-light:#abc' } })
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
    expect(workers[0].messages).toHaveLength(2)

    // The user picks another theme while 'c' is still queued.
    setSyntaxThemePair(newPair)

    // Release a slot so 'c' is posted.
    const first = workers[0].messages[0]
    workers[0].onmessage?.({ data: { id: first.id, html: '<p>a</p>', retryable: false, styles: {} } } as MessageEvent)
    await held[0]

    const posted = workers[0].messages.find(m => m.text === 'c')
    expect(posted?.syntax).toEqual(oldPair)
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
    const { id } = workers[0].messages[0]
    workers[0].onmessage?.({ data: { id, html: '<p>text</p>', retryable: false } } as MessageEvent)
    await expect(pending).resolves.toEqual({ html: '<p>text</p>', retryable: false, styles: {} })
  })
})

import { afterEach, describe, expect, it, vi } from 'vitest'

const originalWorkerDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'Worker')

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

    await expect(renderMarkdownInWorker('no worker')).resolves.toBeNull()
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

    await expect(renderMarkdownInWorker('no worker')).resolves.toBeNull()
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

    const first = renderMarkdownInWorker('first')
    const second = renderMarkdownInWorker('second')

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

    const first = renderMarkdownInWorker('first')
    workers[0].onerror?.()
    await expect(first).resolves.toBeNull()

    const second = renderMarkdownInWorker('second')
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

    const pending = renderMarkdownInWorker('text')
    const { id } = workers[0].messages[0]
    workers[0].onmessage?.({ data: { id, html: '<p>text</p>', retryable: false } } as MessageEvent)
    await expect(pending).resolves.toEqual({ html: '<p>text</p>', retryable: false, styles: {} })
  })
})

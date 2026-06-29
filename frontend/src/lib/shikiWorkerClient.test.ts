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
  return await import('./shikiWorkerClient')
}

describe('shikiWorkerClient', () => {
  afterEach(() => {
    restoreWorker()
  })

  it('returns null when Web Workers are unavailable', async () => {
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: undefined,
    })

    const { tokenizeAsync } = await importClient()

    await expect(tokenizeAsync('bash', 'echo no-worker')).resolves.toBeNull()
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

    const { tokenizeAsync } = await importClient()

    await expect(tokenizeAsync('bash', 'echo no-worker')).resolves.toBeNull()
  })

  it('returns null and terminates the worker when postMessage throws synchronously', async () => {
    const terminateSpy = vi.fn()
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: class PostThrowingWorker {
        onmessage: ((event: MessageEvent) => void) | null = null
        onerror: (() => void) | null = null
        terminate = terminateSpy

        postMessage() {
          throw new Error('worker port closed')
        }
      },
    })

    const { tokenizeAsync } = await importClient()

    await expect(tokenizeAsync('bash', 'echo post-fail')).resolves.toBeNull()
    expect(terminateSpy).toHaveBeenCalledTimes(1)
  })

  it('resolves older pending requests when a later postMessage throws synchronously', async () => {
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

    const { tokenizeAsync } = await importClient()

    const first = tokenizeAsync('bash', 'echo first')
    const second = tokenizeAsync('bash', 'echo second')

    await expect(second).resolves.toBeNull()
    await expect(first).resolves.toBeNull()
    expect(terminateSpy).toHaveBeenCalledTimes(1)
  })

  it('ignores a stale error event from a replaced worker', async () => {
    const tokens = [[{ content: 'second', htmlStyle: {} }]]
    const workers: Array<{
      onmessage: ((event: MessageEvent) => void) | null
      onerror: (() => void) | null
      messages: Array<{ id: number, lang: string, code: string }>
      terminate: ReturnType<typeof vi.fn>
    }> = []
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: class CapturingWorker {
        onmessage: ((event: MessageEvent) => void) | null = null
        onerror: (() => void) | null = null
        messages: Array<{ id: number, lang: string, code: string }> = []
        terminate = vi.fn()

        constructor() {
          workers.push(this)
        }

        postMessage(message: { id: number, lang: string, code: string }) {
          this.messages.push(message)
        }
      },
    })
    const { tokenizeAsync } = await importClient()

    const first = tokenizeAsync('bash', 'echo first')
    workers[0].onerror?.()
    await expect(first).resolves.toBeNull()

    const second = tokenizeAsync('bash', 'echo second')
    let settled = false
    second.then(() => {
      settled = true
    })
    workers[0].onerror?.()
    await Promise.resolve()
    expect(settled).toBe(false)

    const { id } = workers[1].messages[0]
    workers[1].onmessage?.({ data: { id, tokens } } as MessageEvent)
    await expect(second).resolves.toEqual(tokens)
  })
})

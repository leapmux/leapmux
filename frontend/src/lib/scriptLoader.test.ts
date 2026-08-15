/// <reference types="vitest/globals" />
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { loadExternalScript } from './scriptLoader'

function stubScriptElement() {
  const listeners: { onload?: () => void, onerror?: () => void } = {}
  const script = document.createElement('script')
  const define = (name: 'onload' | 'onerror') =>
    Object.defineProperty(script, name, {
      configurable: true,
      get: () => listeners[name],
      set: (fn) => { listeners[name] = fn },
    })
  define('onload')
  define('onerror')
  const originalCreate = document.createElement.bind(document)
  vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
    if (tag === 'script')
      return script
    return originalCreate(tag)
  })
  return {
    fireLoad: () => listeners.onload?.(),
    fireError: () => listeners.onerror?.(),
    script,
  }
}

describe('loadExternalScript', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    // Each test loads a distinct URL so the module-level cache cannot leak
    // between tests.
  })

  it('creates one script tag and resolves on load', async () => {
    const stub = stubScriptElement()
    const headAppend = vi.spyOn(document.head, 'appendChild').mockImplementation(() => stub.script as unknown as Node)

    const promise = loadExternalScript('https://provider.example/api.js?case=ok')
    stub.fireLoad()
    await expect(promise).resolves.toBeUndefined()
    expect(stub.script.src).toBe('https://provider.example/api.js?case=ok')
    expect(stub.script.async).toBe(true)
    expect(headAppend).toHaveBeenCalledOnce()
    headAppend.mockRestore()
  })

  it('shares one load across concurrent callers of the same URL', async () => {
    const stub = stubScriptElement()
    const headAppend = vi.spyOn(document.head, 'appendChild').mockImplementation(() => stub.script as unknown as Node)

    const first = loadExternalScript('https://provider.example/api.js?case=shared')
    const second = loadExternalScript('https://provider.example/api.js?case=shared')
    stub.fireLoad()
    await Promise.all([first, second])
    expect(headAppend).toHaveBeenCalledOnce()
    headAppend.mockRestore()
  })

  it('rejects on error and evicts the cache so a later caller retries', async () => {
    const first = stubScriptElement()
    const headAppend = vi.spyOn(document.head, 'appendChild').mockImplementation(() => first.script as unknown as Node)

    const failing = loadExternalScript('https://provider.example/api.js?case=retry')
    first.fireError()
    await expect(failing).rejects.toThrow('failed to load script')

    // A fresh mount must get a fresh insertion, not the cached rejection.
    const second = stubScriptElement()
    const retry = loadExternalScript('https://provider.example/api.js?case=retry')
    second.fireLoad()
    await expect(retry).resolves.toBeUndefined()
    expect(headAppend).toHaveBeenCalledTimes(2)
    headAppend.mockRestore()
  })
})

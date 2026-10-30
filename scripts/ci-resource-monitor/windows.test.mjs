import { describe, expect, it } from 'bun:test'
import { formatUserResources, readGuiResources } from './windows.mjs'

describe('readGuiResources', () => {
  function nativeGui({ failure = false } = {}) {
    let lastError = 5
    const calls = []
    return {
      calls,
      readLastError: () => lastError,
      kernel32: {
        SetLastError(value) { lastError = value },
        GetLastError() { return lastError },
      },
      user32: {
        GetGuiResources(handle, kind) {
          calls.push([handle, kind])
          if (failure)
            lastError = 5
          return 0
        },
      },
    }
  }

  it.each([
    [1, 2],
    [-1, 2],
    [1, -1],
  ])('queries no GUI resources outside a known current session: %j', (targetSession, monitorSession) => {
    const api = nativeGui()
    expect(readGuiResources(api.user32, 42, { kernel32: api.kernel32, targetSession, monitorSession })).toEqual({ user: -1, gdi: -1 })
    expect(api.calls).toEqual([])
  })

  it('clears stale last errors so a successful zero remains zero', () => {
    const api = nativeGui()
    expect(readGuiResources(api.user32, 42, { kernel32: api.kernel32, targetSession: 1, monitorSession: 1 })).toEqual({ user: 0, gdi: 0 })
    expect(api.readLastError()).toBe(0)
    expect(api.calls).toEqual([
      [42, 1],
      [42, 0],
    ])
  })

  it('keeps failed reads unknown instead of reporting zero', () => {
    const api = nativeGui({ failure: true })
    expect(readGuiResources(api.user32, 42, { kernel32: api.kernel32, targetSession: 1, monitorSession: 1 })).toEqual({ user: -1, gdi: -1 })
  })

  it('keeps successful counts when the other GUI resource read fails', () => {
    const api = nativeGui()
    const setError = api.kernel32.SetLastError
    api.user32.GetGuiResources = (handle, kind) => {
      expect(handle).toBe(42)
      if (kind === 1)
        return 7
      setError(5)
      return 0
    }
    expect(readGuiResources(api.user32, 42, { kernel32: api.kernel32, targetSession: 1, monitorSession: 1 })).toEqual({ user: 7, gdi: -1 })
  })
})

describe('formatUserResources', () => {
  it('exposes incomplete and entirely unknown session coverage', () => {
    expect(formatUserResources([
      {
        session: 0,
        extra: [
          ['user', 7],
        ],
      },
      {
        session: 0,
        extra: [
          ['user', -1],
        ],
      },
      {
        session: 1,
        extra: [
          ['user', -1],
        ],
      },
    ])).toBe('s0:7(unknown=1),s1:unknown')
    expect(formatUserResources([])).toBe('none')
    expect(formatUserResources(null)).toBe('unavailable')
    expect(formatUserResources([
      {
        session: 1,
        extra: [
          ['user', 0],
        ],
      },
    ])).toBe('s1:0')
  })
})

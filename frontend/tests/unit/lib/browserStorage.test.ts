import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DYNAMIC_KEY_TTLS, EXACT_KEY_TTLS, localStorageGet, localStorageRemove, localStorageSet } from '~/lib/browserStorage'

const DAY_MS = 24 * 60 * 60 * 1000
const HOUR_MS = 60 * 60 * 1000
const YEAR_MS = 365 * DAY_MS

describe('browserStorage', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  describe('localStorageSet', () => {
    describe('dynamic keys', () => {
      it('wraps value with { v, e } where e ≈ Date.now() + TTL', () => {
        localStorageSet('leapmux:editor-draft:abc', { content: 'hello', cursor: 5 })
        const raw = localStorage.getItem('leapmux:editor-draft:abc')
        expect(raw).not.toBeNull()
        const parsed = JSON.parse(raw!)
        expect(parsed.v).toEqual({ content: 'hello', cursor: 5 })
        expect(parsed.e).toBe(Date.now() + 7 * DAY_MS)
      })

      it('uses correct TTL per prefix', () => {
        localStorageSet('leapmux:ask-state:a:r', { selections: {} })
        const raw = localStorage.getItem('leapmux:ask-state:a:r')
        const parsed = JSON.parse(raw!)
        expect(parsed.e).toBe(Date.now() + 1 * DAY_MS)
      })

      it('uses correct TTL for each dynamic prefix', () => {
        for (const { prefix, ttlMs } of DYNAMIC_KEY_TTLS) {
          const key = `${prefix}test-id`
          localStorageSet(key, 'test')
          const parsed = JSON.parse(localStorage.getItem(key)!)
          expect(parsed.e).toBe(Date.now() + ttlMs)
        }
      })
    })

    describe('exact keys', () => {
      it('wraps with 1-year TTL', () => {
        localStorageSet('leapmux:mru-agent-providers', ['claude', 'codex'])
        const raw = localStorage.getItem('leapmux:mru-agent-providers')
        const parsed = JSON.parse(raw!)
        expect(parsed.v).toEqual(['claude', 'codex'])
        expect(parsed.e).toBe(Date.now() + YEAR_MS)
      })

      it('uses 1-year TTL for every registered exact key', () => {
        for (const [key, ttlMs] of EXACT_KEY_TTLS) {
          localStorageSet(key, 'test')
          const parsed = JSON.parse(localStorage.getItem(key)!)
          expect(parsed.e, key).toBe(Date.now() + ttlMs)
        }
      })
    })

    describe('unrecognized keys', () => {
      it('throws error for unrecognized leapmux: key', () => {
        expect(() => localStorageSet('leapmux:unknown-key', 'val')).toThrow(/Unknown localStorage key/)
      })

      it('throws error for unrecognized leapmux- key', () => {
        expect(() => localStorageSet('leapmux-unknown-key', 'val')).toThrow(/Unknown localStorage key/)
      })

      it('throws error for non-leapmux key', () => {
        expect(() => localStorageSet('other-key', 'val')).toThrow(/Unknown localStorage key/)
      })
    })
  })

  describe('localStorageGet', () => {
    describe('dynamic keys', () => {
      it('returns v from a valid wrapped value', () => {
        localStorageSet('leapmux:editor-draft:abc', { content: 'hello' })
        const result = localStorageGet<{ content: string }>('leapmux:editor-draft:abc')
        expect(result).toEqual({ content: 'hello' })
      })

      it('returns undefined for unwrapped value (old format)', () => {
        localStorage.setItem('leapmux:editor-draft:abc', JSON.stringify({ content: 'old' }))
        expect(localStorageGet('leapmux:editor-draft:abc')).toBeUndefined()
      })

      it('returns undefined and deletes key when expired', () => {
        const expired = { v: { data: true }, e: Date.now() - 1 }
        localStorage.setItem('leapmux:editor-draft:abc', JSON.stringify(expired))
        expect(localStorageGet('leapmux:editor-draft:abc')).toBeUndefined()
        expect(localStorage.getItem('leapmux:editor-draft:abc')).toBeNull()
      })

      it('refreshes expiration on read when 3+ hours have passed', () => {
        localStorageSet('leapmux:editor-draft:abc', 'data')
        const rawBefore = JSON.parse(localStorage.getItem('leapmux:editor-draft:abc')!)
        const originalExpiry = rawBefore.e

        // Advance 4 hours
        vi.advanceTimersByTime(4 * HOUR_MS)

        localStorageGet('leapmux:editor-draft:abc')
        const rawAfter = JSON.parse(localStorage.getItem('leapmux:editor-draft:abc')!)
        expect(rawAfter.e).toBe(Date.now() + 7 * DAY_MS)
        expect(rawAfter.e).toBeGreaterThan(originalExpiry)
      })

      it('does NOT refresh expiration when < 3 hours have passed', () => {
        localStorageSet('leapmux:editor-draft:abc', 'data')
        const rawBefore = JSON.parse(localStorage.getItem('leapmux:editor-draft:abc')!)
        const originalExpiry = rawBefore.e

        // Advance 2 hours (less than 3-hour threshold)
        vi.advanceTimersByTime(2 * HOUR_MS)

        localStorageGet('leapmux:editor-draft:abc')
        const rawAfter = JSON.parse(localStorage.getItem('leapmux:editor-draft:abc')!)
        expect(rawAfter.e).toBe(originalExpiry) // unchanged
      })

      it('returns undefined for missing key', () => {
        expect(localStorageGet('leapmux:editor-draft:nonexistent')).toBeUndefined()
      })
    })

    describe('exact keys', () => {
      it('returns v from a valid wrapped value', () => {
        localStorageSet('leapmux:mru-agent-providers', ['claude'])
        expect(localStorageGet('leapmux:mru-agent-providers')).toEqual(['claude'])
      })

      it('returns undefined for missing key', () => {
        expect(localStorageGet('leapmux:key-pins')).toBeUndefined()
      })

      it('refreshes expiration on read across a long inactivity gap', () => {
        // The 1-year TTL plus refresh-on-read is the "never expires while
        // actively used" contract for preferences/trust state. After a
        // 6-month idle window, the next read should push expiration back
        // out to a year from now.
        localStorageSet('leapmux:preferred-editor', 'vscode')
        vi.advanceTimersByTime(180 * DAY_MS)
        localStorageGet('leapmux:preferred-editor')
        const rawAfter = JSON.parse(localStorage.getItem('leapmux:preferred-editor')!)
        expect(rawAfter.e).toBe(Date.now() + YEAR_MS)
      })
    })

    describe('unrecognized keys', () => {
      it('throws error', () => {
        expect(() => localStorageGet('leapmux:unknown')).toThrow(/Unknown localStorage key/)
        expect(() => localStorageGet('other-key')).toThrow(/Unknown localStorage key/)
      })
    })
  })

  describe('localStorageRemove', () => {
    it('removes the key without validation', () => {
      localStorage.setItem('leapmux:key-pins', 'test-value')
      localStorageRemove('leapmux:key-pins')
      expect(localStorage.getItem('leapmux:key-pins')).toBeNull()
    })

    it('does not throw for non-existent keys', () => {
      expect(() => localStorageRemove('leapmux:nonexistent')).not.toThrow()
    })

    it('removes dynamic wrapped keys', () => {
      localStorageSet('leapmux:editor-draft:abc', { content: 'hello' })
      localStorageRemove('leapmux:editor-draft:abc')
      expect(localStorage.getItem('leapmux:editor-draft:abc')).toBeNull()
    })
  })
})

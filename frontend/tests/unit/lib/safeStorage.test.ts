import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { safeGetJson, safeGetString, safeRemoveItem, safeSetJson, safeSetString } from '~/lib/safeStorage'
import { DYNAMIC_KEY_TTLS } from '~/lib/storageCleanup'

const DAY_MS = 24 * 60 * 60 * 1000
const HOUR_MS = 60 * 60 * 1000

describe('safeStorage', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  describe('safeSetJson', () => {
    describe('dynamic keys', () => {
      it('wraps value with { v, e } where e ≈ Date.now() + TTL', () => {
        safeSetJson('leapmux:editor-draft:abc', { content: 'hello', cursor: 5 })
        const raw = localStorage.getItem('leapmux:editor-draft:abc')
        expect(raw).not.toBeNull()
        const parsed = JSON.parse(raw!)
        expect(parsed.v).toEqual({ content: 'hello', cursor: 5 })
        expect(parsed.e).toBe(Date.now() + 7 * DAY_MS)
      })

      it('uses correct TTL per prefix', () => {
        safeSetJson('leapmux:ask-state:a:r', { selections: {} })
        const raw = localStorage.getItem('leapmux:ask-state:a:r')
        const parsed = JSON.parse(raw!)
        expect(parsed.e).toBe(Date.now() + 1 * DAY_MS)
      })

      it('uses correct TTL for each dynamic prefix', () => {
        for (const { prefix, ttlMs } of DYNAMIC_KEY_TTLS) {
          const key = `${prefix}test-id`
          safeSetJson(key, 'test')
          const parsed = JSON.parse(localStorage.getItem(key)!)
          expect(parsed.e).toBe(Date.now() + ttlMs)
        }
      })
    })

    describe('static keys', () => {
      it('stores raw JSON without wrapping', () => {
        safeSetJson('leapmux:mru-agent-providers', ['claude', 'codex'])
        const raw = localStorage.getItem('leapmux:mru-agent-providers')
        expect(JSON.parse(raw!)).toEqual(['claude', 'codex'])
      })

      it('stores string values raw', () => {
        safeSetJson('leapmux:key-pins', { w1: { publicKeyHex: 'aa', firstSeen: 1 } })
        const raw = localStorage.getItem('leapmux:key-pins')
        const parsed = JSON.parse(raw!)
        expect(parsed.w1.publicKeyHex).toBe('aa')
        expect(parsed).not.toHaveProperty('v')
        expect(parsed).not.toHaveProperty('e')
      })
    })

    describe('unrecognized keys', () => {
      it('throws error for unrecognized leapmux: key', () => {
        expect(() => safeSetJson('leapmux:unknown-key', 'val')).toThrow(/Unknown localStorage key/)
      })

      it('throws error for unrecognized leapmux- key', () => {
        expect(() => safeSetJson('leapmux-unknown-key', 'val')).toThrow(/Unknown localStorage key/)
      })

      it('throws error for non-leapmux key', () => {
        expect(() => safeSetJson('other-key', 'val')).toThrow(/Unknown localStorage key/)
      })
    })
  })

  describe('safeGetJson', () => {
    describe('dynamic keys', () => {
      it('returns v from a valid wrapped value', () => {
        safeSetJson('leapmux:editor-draft:abc', { content: 'hello' })
        const result = safeGetJson<{ content: string }>('leapmux:editor-draft:abc')
        expect(result).toEqual({ content: 'hello' })
      })

      it('returns undefined for unwrapped value (old format)', () => {
        localStorage.setItem('leapmux:editor-draft:abc', JSON.stringify({ content: 'old' }))
        expect(safeGetJson('leapmux:editor-draft:abc')).toBeUndefined()
      })

      it('returns undefined and deletes key when expired', () => {
        const expired = { v: { data: true }, e: Date.now() - 1 }
        localStorage.setItem('leapmux:editor-draft:abc', JSON.stringify(expired))
        expect(safeGetJson('leapmux:editor-draft:abc')).toBeUndefined()
        expect(localStorage.getItem('leapmux:editor-draft:abc')).toBeNull()
      })

      it('refreshes expiration on read when 3+ hours have passed', () => {
        safeSetJson('leapmux:editor-draft:abc', 'data')
        const rawBefore = JSON.parse(localStorage.getItem('leapmux:editor-draft:abc')!)
        const originalExpiry = rawBefore.e

        // Advance 4 hours
        vi.advanceTimersByTime(4 * HOUR_MS)

        safeGetJson('leapmux:editor-draft:abc')
        const rawAfter = JSON.parse(localStorage.getItem('leapmux:editor-draft:abc')!)
        expect(rawAfter.e).toBe(Date.now() + 7 * DAY_MS)
        expect(rawAfter.e).toBeGreaterThan(originalExpiry)
      })

      it('does NOT refresh expiration when < 3 hours have passed', () => {
        safeSetJson('leapmux:editor-draft:abc', 'data')
        const rawBefore = JSON.parse(localStorage.getItem('leapmux:editor-draft:abc')!)
        const originalExpiry = rawBefore.e

        // Advance 2 hours (less than 3-hour threshold)
        vi.advanceTimersByTime(2 * HOUR_MS)

        safeGetJson('leapmux:editor-draft:abc')
        const rawAfter = JSON.parse(localStorage.getItem('leapmux:editor-draft:abc')!)
        expect(rawAfter.e).toBe(originalExpiry) // unchanged
      })

      it('returns undefined for missing key', () => {
        expect(safeGetJson('leapmux:editor-draft:nonexistent')).toBeUndefined()
      })
    })

    describe('static keys', () => {
      it('returns raw parsed value without unwrapping', () => {
        safeSetJson('leapmux:mru-agent-providers', ['claude'])
        expect(safeGetJson('leapmux:mru-agent-providers')).toEqual(['claude'])
      })

      it('returns undefined for missing key', () => {
        expect(safeGetJson('leapmux:key-pins')).toBeUndefined()
      })
    })

    describe('unrecognized keys', () => {
      it('throws error', () => {
        expect(() => safeGetJson('leapmux:unknown')).toThrow(/Unknown localStorage key/)
        expect(() => safeGetJson('other-key')).toThrow(/Unknown localStorage key/)
      })
    })
  })

  describe('safeSetString', () => {
    describe('dynamic keys', () => {
      it('wraps string value as JSON { v, e }', () => {
        safeSetString('leapmux:editor-min-height:abc', '120')
        const raw = localStorage.getItem('leapmux:editor-min-height:abc')
        const parsed = JSON.parse(raw!)
        expect(parsed.v).toBe('120')
        expect(parsed.e).toBe(Date.now() + 7 * DAY_MS)
      })
    })

    describe('static keys', () => {
      it('stores raw string via localStorage.setItem', () => {
        safeSetString('leapmux:theme', 'dark')
        expect(localStorage.getItem('leapmux:theme')).toBe('dark')
      })
    })

    describe('unrecognized keys', () => {
      it('throws error', () => {
        expect(() => safeSetString('leapmux:unknown', 'val')).toThrow(/Unknown localStorage key/)
      })
    })
  })

  describe('safeGetString', () => {
    describe('dynamic keys', () => {
      it('parses JSON, unwraps, returns v as string', () => {
        safeSetString('leapmux:editor-min-height:abc', '120')
        expect(safeGetString('leapmux:editor-min-height:abc')).toBe('120')
      })

      it('returns null for expired entries and deletes them', () => {
        const expired = { v: '120', e: Date.now() - 1 }
        localStorage.setItem('leapmux:editor-min-height:abc', JSON.stringify(expired))
        expect(safeGetString('leapmux:editor-min-height:abc')).toBeNull()
        expect(localStorage.getItem('leapmux:editor-min-height:abc')).toBeNull()
      })

      it('returns null for unwrapped entries (old format)', () => {
        localStorage.setItem('leapmux:editor-min-height:abc', '120')
        expect(safeGetString('leapmux:editor-min-height:abc')).toBeNull()
      })

      it('refreshes expiration when 3+ hours have passed', () => {
        safeSetString('leapmux:editor-min-height:abc', '120')
        vi.advanceTimersByTime(4 * HOUR_MS)
        safeGetString('leapmux:editor-min-height:abc')
        const rawAfter = JSON.parse(localStorage.getItem('leapmux:editor-min-height:abc')!)
        expect(rawAfter.e).toBe(Date.now() + 7 * DAY_MS)
      })
    })

    describe('static keys', () => {
      it('returns raw localStorage.getItem result', () => {
        safeSetString('leapmux:theme', 'dark')
        expect(safeGetString('leapmux:theme')).toBe('dark')
      })

      it('returns null for missing key', () => {
        expect(safeGetString('leapmux:theme')).toBeNull()
      })

      it('does NOT refresh expiration (static keys have no TTL)', () => {
        safeSetString('leapmux:theme', 'dark')
        vi.advanceTimersByTime(4 * HOUR_MS)
        // Static key — stored as raw string, no wrapping, no refresh
        expect(localStorage.getItem('leapmux:theme')).toBe('dark')
        expect(safeGetString('leapmux:theme')).toBe('dark')
      })
    })

    describe('unrecognized keys', () => {
      it('throws error', () => {
        expect(() => safeGetString('leapmux:unknown')).toThrow(/Unknown localStorage key/)
      })
    })
  })

  describe('safeRemoveItem', () => {
    it('removes the key without validation', () => {
      localStorage.setItem('leapmux:theme', 'dark')
      safeRemoveItem('leapmux:theme')
      expect(localStorage.getItem('leapmux:theme')).toBeNull()
    })

    it('does not throw for non-existent keys', () => {
      expect(() => safeRemoveItem('leapmux:nonexistent')).not.toThrow()
    })

    it('removes dynamic wrapped keys', () => {
      safeSetJson('leapmux:editor-draft:abc', { content: 'hello' })
      safeRemoveItem('leapmux:editor-draft:abc')
      expect(localStorage.getItem('leapmux:editor-draft:abc')).toBeNull()
    })
  })
})

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  DYNAMIC_KEY_TTLS,
  getTtlForKey,
  initStorageCleanup,
  isWrappedValue,
  runCleanup,
  STATIC_KEYS,
} from '~/lib/storageCleanup'

describe('storageCleanup', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  describe('getTtlForKey', () => {
    it('returns correct TTL for each dynamic prefix', () => {
      for (const { prefix, ttlMs } of DYNAMIC_KEY_TTLS) {
        expect(getTtlForKey(`${prefix}some-id`)).toBe(ttlMs)
      }
    })

    it('returns null for known static keys', () => {
      for (const key of STATIC_KEYS) {
        expect(getTtlForKey(key)).toBeNull()
      }
    })

    it('returns null for keys that do not start with leapmux: or leapmux-', () => {
      expect(getTtlForKey('some-other-key')).toBeNull()
      expect(getTtlForKey('theme')).toBeNull()
    })
  })

  describe('isWrappedValue', () => {
    it('returns true for valid wrapped values', () => {
      expect(isWrappedValue({ v: 'hello', e: 123 })).toBe(true)
      expect(isWrappedValue({ v: null, e: 0 })).toBe(true)
      expect(isWrappedValue({ v: { nested: true }, e: 999 })).toBe(true)
      expect(isWrappedValue({ v: 42, e: Date.now() })).toBe(true)
    })

    it('returns false for invalid values', () => {
      expect(isWrappedValue('plain string')).toBe(false)
      expect(isWrappedValue({ v: 'hello' })).toBe(false)
      expect(isWrappedValue({ e: 123 })).toBe(false)
      expect(isWrappedValue(null)).toBe(false)
      expect(isWrappedValue(undefined)).toBe(false)
      expect(isWrappedValue(42)).toBe(false)
      expect(isWrappedValue([])).toBe(false)
      expect(isWrappedValue({ v: 'hello', e: 'not a number' })).toBe(false)
    })
  })

  describe('runCleanup', () => {
    it('deletes expired wrapped dynamic keys', () => {
      const expired = { v: 'data', e: Date.now() - 1000 }
      localStorage.setItem('leapmux:editor-draft:abc', JSON.stringify(expired))
      runCleanup()
      expect(localStorage.getItem('leapmux:editor-draft:abc')).toBeNull()
    })

    it('deletes unwrapped dynamic keys (old format)', () => {
      localStorage.setItem('leapmux:editor-draft:abc', '"raw string"')
      runCleanup()
      expect(localStorage.getItem('leapmux:editor-draft:abc')).toBeNull()
    })

    it('deletes old dash-convention keys', () => {
      localStorage.setItem('leapmux-editor-draft-abc', '"raw"')
      localStorage.setItem('leapmux-theme', 'dark')
      runCleanup()
      expect(localStorage.getItem('leapmux-editor-draft-abc')).toBeNull()
      expect(localStorage.getItem('leapmux-theme')).toBeNull()
    })

    it('deletes unrecognized leapmux: keys', () => {
      localStorage.setItem('leapmux:some-unknown-key', '"data"')
      runCleanup()
      expect(localStorage.getItem('leapmux:some-unknown-key')).toBeNull()
    })

    it('preserves static keys', () => {
      for (const key of STATIC_KEYS) {
        localStorage.setItem(key, '"test-value"')
      }
      runCleanup()
      for (const key of STATIC_KEYS) {
        expect(localStorage.getItem(key)).toBe('"test-value"')
      }
    })

    it('preserves fresh (non-expired) wrapped dynamic keys', () => {
      const fresh = { v: 'data', e: Date.now() + 7 * 24 * 60 * 60 * 1000 }
      localStorage.setItem('leapmux:editor-draft:abc', JSON.stringify(fresh))
      runCleanup()
      expect(localStorage.getItem('leapmux:editor-draft:abc')).not.toBeNull()
    })

    it('preserves non-leapmux keys', () => {
      localStorage.setItem('other-app-key', 'some value')
      localStorage.setItem('random', '123')
      runCleanup()
      expect(localStorage.getItem('other-app-key')).toBe('some value')
      expect(localStorage.getItem('random')).toBe('123')
    })
  })

  describe('initStorageCleanup', () => {
    it('runs cleanup immediately on init', () => {
      localStorage.setItem('leapmux-old-key', 'stale')
      const dispose = initStorageCleanup()
      expect(localStorage.getItem('leapmux-old-key')).toBeNull()
      dispose()
    })

    it('returns a dispose function that clears the interval', () => {
      const dispose = initStorageCleanup()
      // Add a stale key after init cleanup ran
      localStorage.setItem('leapmux-stale', 'data')

      // Advance time by 1 hour — should trigger cleanup
      vi.advanceTimersByTime(60 * 60 * 1000)
      expect(localStorage.getItem('leapmux-stale')).toBeNull()

      // After dispose, cleanup should not run
      localStorage.setItem('leapmux-stale2', 'data')
      dispose()
      vi.advanceTimersByTime(60 * 60 * 1000)
      expect(localStorage.getItem('leapmux-stale2')).toBe('data')
    })

    it('sets up hourly interval', () => {
      const dispose = initStorageCleanup()

      // Add an expired dynamic key
      const expired = { v: 'data', e: Date.now() - 1 }
      localStorage.setItem('leapmux:ask-state:agent:req', JSON.stringify(expired))

      // Wait, that's already expired — it would have been cleaned in init.
      // Let's add one that expires after 30 minutes.
      vi.advanceTimersByTime(30 * 60 * 1000) // advance 30min
      const soonExpired = { v: 'data', e: Date.now() + 1000 } // expires in 1s
      localStorage.setItem('leapmux:ask-state:agent:req2', JSON.stringify(soonExpired))

      // Advance past expiration but not yet to next cleanup
      vi.advanceTimersByTime(2000) // 1s past expiration, 30min+2s total
      // Not yet cleaned — cleanup hasn't run
      expect(localStorage.getItem('leapmux:ask-state:agent:req2')).not.toBeNull()

      // Advance to the 1-hour mark
      vi.advanceTimersByTime(30 * 60 * 1000 - 2000) // now at 60min
      expect(localStorage.getItem('leapmux:ask-state:agent:req2')).toBeNull()

      dispose()
    })
  })
})

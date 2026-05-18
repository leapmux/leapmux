import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  EXACT_KEY_TTLS,
  getSessionTtlForKey,
  getTtlForKey,
  initStorageCleanup,
  isWrappedValue,
  KEY_CLIENT_ID,
  localStorageSet,
  runCleanup,
  sessionStorageSet,
} from '~/lib/browserStorage'

const DAY_MS = 24 * 60 * 60 * 1000
const YEAR_MS = 365 * DAY_MS

describe('storageCleanup', () => {
  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
    vi.useFakeTimers()
  })

  afterEach(() => {
    sessionStorage.clear()
    vi.useRealTimers()
  })

  describe('getTtlForKey', () => {
    it('returns the registered TTL for each dynamic prefix', () => {
      // Pin the prefix/TTL pairs so a regression in DYNAMIC_KEY_TTLS (wrong
      // prefix, wrong number-of-days multiplier, missing entry) is caught.
      // Iterating DYNAMIC_KEY_TTLS itself would only verify prefix-matching
      // works, not that the TTL values are correct.
      expect(getTtlForKey('leapmux:editor-draft:abc')).toBe(7 * DAY_MS)
      expect(getTtlForKey('leapmux:editor-min-height:abc')).toBe(7 * DAY_MS)
      expect(getTtlForKey('leapmux:agent-session:abc')).toBe(7 * DAY_MS)
      expect(getTtlForKey('leapmux:ask-state:agent:req')).toBe(1 * DAY_MS)
      expect(getTtlForKey('leapmux:worker-info:abc')).toBe(7 * DAY_MS)
      expect(getTtlForKey('leapmux:local-messages:abc')).toBe(7 * DAY_MS)
      expect(getTtlForKey('leapmux:files-show-hidden:abc')).toBe(7 * DAY_MS)
    })

    it('returns 1-year TTL for every registered exact localStorage key', () => {
      for (const [key, ttlMs] of EXACT_KEY_TTLS) {
        expect(getTtlForKey(key), key).toBe(ttlMs)
        expect(ttlMs).toBe(YEAR_MS)
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

    it('preserves wrapped exact localStorage keys', () => {
      for (const key of EXACT_KEY_TTLS.keys())
        localStorageSet(key, 'test-value')
      runCleanup()
      for (const key of EXACT_KEY_TTLS.keys())
        expect(localStorage.getItem(key), key).not.toBeNull()
    })

    it('deletes unwrapped exact localStorage keys (legacy raw format)', () => {
      for (const key of EXACT_KEY_TTLS.keys())
        localStorage.setItem(key, '"raw-legacy-value"')
      runCleanup()
      for (const key of EXACT_KEY_TTLS.keys())
        expect(localStorage.getItem(key), key).toBeNull()
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

    // Regression: every per-feature `leapmux:`-prefixed sessionStorage
    // key must be registered (SESSION_DYNAMIC_KEY_TTLS for templated
    // keys, SESSION_EXACT_KEY_TTLS for singletons), otherwise the sweep
    // wipes it on the next page load. The original instance of this bug
    // was `useTabPersistence` losing the active tab on every refresh,
    // but the same trap applied to sidebar widths, the workspace-tree
    // expansion set, the tab-tree collapse state, the per-session client
    // id, the directory-tree expansion state, and the CLI-path one-shot.
    it('preserves every registered sessionStorage key under runCleanup', () => {
      const samples: Array<[string, unknown]> = [
        ['leapmux:activeTab:ws-1', '1:agent-abc'],
        ['leapmux:tileActiveTabs:ws-1', { tile: '1:agent-abc' }],
        ['leapmux:focusedTile:ws-1', 'tile-1'],
        ['leapmux:activeWorkspace', 'ws-1'],
        ['leapmux:cli-path-checked', true],
        ['leapmux:sidebar:ws-1', { leftSize: 240 }],
        ['leapmux:expandedWorkspaces', ['ws-1', 'ws-2']],
        ['leapmux:tabTree:ws-1', { group: true }],
        ['leapmux:client-id', 'c-abcdef'],
        ['leapmux:directoryTree:~:files', { expandedPaths: {} }],
        ['leapmux:fileScroll:abc', 42],
      ]
      for (const [key, value] of samples)
        sessionStorageSet(key, value)

      runCleanup()

      for (const [key] of samples)
        expect(sessionStorage.getItem(key), key).not.toBeNull()
    })

    // Singleton sessionStorage keys live in SESSION_EXACT_KEY_TTLS and
    // are matched by exact string. A neighbour key whose name starts
    // with the singleton (e.g. `${KEY_CLIENT_ID}-extra`) must NOT
    // inherit the singleton's TTL via prefix matching — that's the
    // whole reason the exact-match table exists.
    it('does not bleed exact-match TTLs into prefix-matched neighbours', () => {
      expect(getSessionTtlForKey(KEY_CLIENT_ID)).not.toBeNull()
      expect(getSessionTtlForKey(`${KEY_CLIENT_ID}-extra`)).toBeNull()
      expect(getSessionTtlForKey(`${KEY_CLIENT_ID}:foo`)).toBeNull()
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

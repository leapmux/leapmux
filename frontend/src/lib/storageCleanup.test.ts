import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  accountStorageKey,
  getSessionTtlForStoredKey,
  getTtlForKey,
  getTtlForStoredKey,
  initStorageCleanup,
  isWrappedValue,
  KEY_CHANNEL_RELAY_SEQ,
  KEY_CLIENT_ID,
  LOCAL_KEY_SPECS,
  localStorageSet,
  resetStorageAccountForTests,
  runCleanup,
  SESSION_KEY_SPECS,
  sessionStorageSet,
  setStorageAccount,
  storedKeyFor,
} from '~/lib/browserStorage'
import { TEST_USER_ID } from '~/test-support/crdtBridge'

// Restated rather than imported, so an assertion is an INDEPENDENT statement of
// the number the registry holds. Importing the module's own constants would
// make each one compare a value against itself.
const DAY_MS = 24 * 60 * 60 * 1000
const YEAR_MS = 365 * DAY_MS

// The account `vitest.setup.ts` signs the suite in as. Taken from there rather
// than spelled again, because it is not an expectation of this file's own -- it
// is the identity every read and write here resolves under.
const ACCOUNT = TEST_USER_ID
const OTHER = 'otheraccount'

/** A fresh `{v,e}` envelope, written straight to the store under a stored key. */
function writeFresh(storage: Storage, stored: string, ttlMs = 7 * DAY_MS): void {
  storage.setItem(stored, JSON.stringify({ v: 'data', e: Date.now() + ttlMs }))
}

describe('storageCleanup', () => {
  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
    vi.useFakeTimers()
  })

  afterEach(() => {
    sessionStorage.clear()
    vi.useRealTimers()
    setStorageAccount(ACCOUNT)
  })

  describe('getTtlForKey', () => {
    it('returns the registered TTL for each dynamic prefix', () => {
      // Pin the prefix/TTL pairs so a regression (wrong prefix, wrong
      // number-of-days multiplier, missing entry) is caught. Iterating the
      // table itself would only verify prefix-matching works, not that the TTL
      // values are correct.
      expect(getTtlForKey('editor-draft:abc')).toBe(7 * DAY_MS)
      expect(getTtlForKey('editor-min-height:abc')).toBe(7 * DAY_MS)
      expect(getTtlForKey('agent-session:abc')).toBe(7 * DAY_MS)
      expect(getTtlForKey('ask-state:agent:req')).toBe(1 * DAY_MS)
      expect(getTtlForKey('worker-info:abc')).toBe(7 * DAY_MS)
      expect(getTtlForKey('local-messages:abc')).toBe(7 * DAY_MS)
      expect(getTtlForKey('files-show-hidden:abc')).toBe(7 * DAY_MS)
      expect(getTtlForKey('files-sort-order:abc')).toBe(7 * DAY_MS)
    })

    // Same reasoning as the prefixes above, for the singletons. Iterating the
    // table and comparing each entry against its own `ttlMs` passes for ANY
    // number, so it pins the lookup and not the value. These are the values:
    // a year, plus the on-read refresh, means a user who opens the app at any
    // point during a year keeps them. A copy-paste of the 30-day session TTL
    // into any of them silently drops a returning user's key pins, preferences
    // or last workspace.
    it('gives every long-lived localStorage singleton a year', () => {
      expect(getTtlForKey('browser-prefs')).toBe(YEAR_MS)
      expect(getTtlForKey('mru-agent-providers')).toBe(YEAR_MS)
      expect(getTtlForKey('key-pins')).toBe(YEAR_MS)
      expect(getTtlForKey('directory-selector-show-hidden')).toBe(YEAR_MS)
      expect(getTtlForKey('preferred-editor')).toBe(YEAR_MS)
      expect(getTtlForKey('user-events-relay-seq')).toBe(YEAR_MS)
      expect(getTtlForKey('channel-relay-seq')).toBe(YEAR_MS)
      // The odd one out among the templated table-mates, and deliberately so:
      // it is a preference rather than a cache, and it is the only record of
      // which workspace to reopen now that the URL carries no workspace id. A
      // day-scale TTL here would silently drop a returning user on workspace #1.
      expect(getTtlForKey('active-workspace')).toBe(YEAR_MS)
    })

    it('returns null for an unregistered name', () => {
      expect(getTtlForKey('some-other-key')).toBeNull()
      expect(getTtlForKey('theme')).toBeNull()
      // The stored form is not a logical name.
      expect(getTtlForKey('leapmux:browser-prefs')).toBeNull()
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

  describe('getTtlForStoredKey', () => {
    it('resolves an account-scoped stored key for ANY account', () => {
      expect(getTtlForStoredKey(accountStorageKey(OTHER, 'editor-draft:a'))).toBe(7 * DAY_MS)
    })

    it('resolves a device-scoped stored key', () => {
      expect(getTtlForStoredKey('leapmux:channel-relay-seq')).toBe(YEAR_MS)
    })

    // A scope mismatch is UNKNOWN, not a fallback. That is what retires a flat
    // key left by an earlier build, and what stops a scoped copy of a device
    // key from passing as registered.
    it('refuses a key stored under the wrong scope', () => {
      expect(getTtlForStoredKey('leapmux:browser-prefs')).toBeNull()
      expect(getTtlForStoredKey(accountStorageKey(ACCOUNT, 'channel-relay-seq'))).toBeNull()
    })

    it('refuses a malformed account segment', () => {
      expect(getTtlForStoredKey('leapmux:u:')).toBeNull()
      expect(getTtlForStoredKey('leapmux:u:abc')).toBeNull()
      expect(getTtlForStoredKey('leapmux:u::browser-prefs')).toBeNull()
      // A percent escape this module could not have written.
      expect(getTtlForStoredKey('leapmux:u:bad%ZZ:browser-prefs')).toBeNull()
    })

    // The id segment is percent-encoded, so the sweep answers for a key of ANY
    // account whatever the hub's id format is. The alternative -- reject an id
    // outside `[A-Za-z0-9]` -- restates the backend's alphabet in the frontend
    // and deletes a legitimate key the day it widens.
    it('resolves an account segment holding a separator or a non-ASCII id', () => {
      expect(getTtlForStoredKey(accountStorageKey('has-hyphen', 'browser-prefs'))).toBe(YEAR_MS)
      expect(getTtlForStoredKey(accountStorageKey('a:b', 'browser-prefs'))).toBe(YEAR_MS)
      expect(getTtlForStoredKey(accountStorageKey('사용자', 'browser-prefs'))).toBe(YEAR_MS)
      // The encoded key still holds exactly one bare separator after `u:`.
      expect(accountStorageKey('a:b', 'browser-prefs')).toBe('leapmux:u:a%3Ab:browser-prefs')
    })

    it('refuses a registered name under an unregistered one', () => {
      expect(getTtlForStoredKey(accountStorageKey(ACCOUNT, 'not-registered'))).toBeNull()
    })

    it('resolves the session table separately from the local one', () => {
      expect(getSessionTtlForStoredKey(accountStorageKey(ACCOUNT, 'tab-mru'))).not.toBeNull()
      // A localStorage name is not a sessionStorage name.
      expect(getSessionTtlForStoredKey(accountStorageKey(ACCOUNT, 'browser-prefs'))).toBeNull()
    })
  })

  describe('runCleanup', () => {
    it('deletes expired wrapped dynamic keys', () => {
      localStorage.setItem(storedKeyFor('editor-draft:abc')!, JSON.stringify({ v: 'data', e: Date.now() - 1000 }))
      runCleanup()
      expect(localStorage.getItem(storedKeyFor('editor-draft:abc')!)).toBeNull()
    })

    it('deletes unwrapped dynamic keys (old format)', () => {
      localStorage.setItem(storedKeyFor('editor-draft:abc')!, '"raw string"')
      runCleanup()
      expect(localStorage.getItem(storedKeyFor('editor-draft:abc')!)).toBeNull()
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

    it('preserves fresh (non-expired) wrapped dynamic keys', () => {
      writeFresh(localStorage, storedKeyFor('editor-draft:abc')!)
      runCleanup()
      expect(localStorage.getItem(storedKeyFor('editor-draft:abc')!)).not.toBeNull()
    })

    it('preserves non-leapmux keys', () => {
      localStorage.setItem('other-app-key', 'some value')
      localStorage.setItem('random', '123')
      runCleanup()
      expect(localStorage.getItem('other-app-key')).toBe('some value')
      expect(localStorage.getItem('random')).toBe('123')
    })

    // THE POINT OF SCOPING. A sweep that judged keys against the signed-in
    // account would wipe every other account's state on each page load, so the
    // second account on a browser would be reset by the first one opening the
    // app.
    it('preserves a fresh key belonging to a DIFFERENT account', () => {
      const theirs = accountStorageKey(OTHER, 'browser-prefs')
      writeFresh(localStorage, theirs, YEAR_MS)
      runCleanup()
      expect(localStorage.getItem(theirs)).not.toBeNull()
    })

    // Kept, but not exempt: an account nobody signs into still ages out.
    it('deletes an EXPIRED key belonging to a different account', () => {
      const theirs = accountStorageKey(OTHER, 'browser-prefs')
      localStorage.setItem(theirs, JSON.stringify({ v: 'data', e: Date.now() - 1 }))
      runCleanup()
      expect(localStorage.getItem(theirs)).toBeNull()
    })

    // The migration, such as it is: the move to scoped keys retires every flat
    // copy on the first sweep, with no migration code.
    it('deletes a flat copy of a now-scoped key, however fresh', () => {
      writeFresh(localStorage, 'leapmux:browser-prefs', YEAR_MS)
      writeFresh(localStorage, 'leapmux:key-pins', YEAR_MS)
      writeFresh(localStorage, 'leapmux:activeWorkspace:user-1', YEAR_MS)
      runCleanup()
      expect(localStorage.getItem('leapmux:browser-prefs')).toBeNull()
      expect(localStorage.getItem('leapmux:key-pins')).toBeNull()
      expect(localStorage.getItem('leapmux:activeWorkspace:user-1')).toBeNull()
    })

    it('deletes a scoped copy of a device-scoped key', () => {
      writeFresh(localStorage, accountStorageKey(ACCOUNT, 'channel-relay-seq'), YEAR_MS)
      runCleanup()
      expect(localStorage.getItem(accountStorageKey(ACCOUNT, 'channel-relay-seq'))).toBeNull()
    })

    it('deletes a malformed account segment', () => {
      for (const stored of ['leapmux:u:', 'leapmux:u:abc', 'leapmux:u::browser-prefs'])
        writeFresh(localStorage, stored, YEAR_MS)
      runCleanup()
      for (const stored of ['leapmux:u:', 'leapmux:u:abc', 'leapmux:u::browser-prefs'])
        expect(localStorage.getItem(stored), stored).toBeNull()
    })

    // The sweep runs from `app.tsx` before any provider mounts and before the
    // auth bootstrap answers, so it is the one storage caller that must work
    // with no account at all.
    it('runs with no storage account set, and keeps the device keys', () => {
      writeFresh(localStorage, 'leapmux:channel-relay-seq', YEAR_MS)
      const theirs = accountStorageKey(OTHER, 'browser-prefs')
      writeFresh(localStorage, theirs, YEAR_MS)

      resetStorageAccountForTests()
      expect(() => runCleanup()).not.toThrow()

      expect(localStorage.getItem('leapmux:channel-relay-seq')).not.toBeNull()
      expect(localStorage.getItem(theirs)).not.toBeNull()
    })

    // Regression: every per-feature `leapmux:`-prefixed sessionStorage key must
    // be registered, otherwise the sweep wipes it on the next page load. The
    // original instance of this bug was `useTabPersistence` losing the active
    // tab on every refresh, but the same trap applied to sidebar widths, the
    // workspace-tree expansion set, the tab-tree collapse state, the
    // per-session client id, the directory-tree expansion state, and the
    // CLI-path one-shot.
    //
    // The samples are DERIVED from the registry rather than restated, so a key
    // added to the table is covered without anyone remembering to add it here.
    it('preserves every registered sessionStorage key under runCleanup', () => {
      const names = Object.entries(SESSION_KEY_SPECS)
        .map(([name, spec]) => (spec.match === 'prefix' ? `${name}sample` : name))
      for (const name of names)
        sessionStorageSet(name, 'sample')

      runCleanup()

      expect(names.length).toBeGreaterThan(0)
      for (const name of names)
        expect(sessionStorage.getItem(storedKeyFor(name)!), name).not.toBeNull()
    })

    it('preserves every registered localStorage key under runCleanup', () => {
      const names = Object.entries(LOCAL_KEY_SPECS)
        .map(([name, spec]) => (spec.match === 'prefix' ? `${name}sample` : name))
      for (const name of names)
        localStorageSet(name, 'sample')

      runCleanup()

      expect(names.length).toBeGreaterThan(0)
      for (const name of names)
        expect(localStorage.getItem(storedKeyFor(name)!), name).not.toBeNull()
    })

    it('deletes unwrapped copies of every registered localStorage key', () => {
      const names = Object.keys(LOCAL_KEY_SPECS).filter(n => !n.endsWith(':'))
      for (const name of names)
        localStorage.setItem(storedKeyFor(name)!, '"raw-legacy-value"')
      runCleanup()
      for (const name of names)
        expect(localStorage.getItem(storedKeyFor(name)!), name).toBeNull()
    })

    // Singleton sessionStorage keys are matched by exact string. A neighbour
    // whose name starts with the singleton must NOT inherit its TTL via prefix
    // matching — that is the whole reason the exact match exists.
    it('does not bleed exact-match TTLs into prefix-matched neighbours', () => {
      expect(getSessionTtlForStoredKey(accountStorageKey(ACCOUNT, KEY_CLIENT_ID))).not.toBeNull()
      expect(getSessionTtlForStoredKey(accountStorageKey(ACCOUNT, `${KEY_CLIENT_ID}-extra`))).toBeNull()
      expect(getSessionTtlForStoredKey(accountStorageKey(ACCOUNT, `${KEY_CLIENT_ID}:foo`))).toBeNull()
    })

    // Every session singleton, pinned by value for the same reason the
    // localStorage ones are: iterating the table proves only that the lookup
    // works.
    it('gives every sessionStorage singleton its registered TTL', () => {
      const ttl = (name: string) => getSessionTtlForStoredKey(accountStorageKey(ACCOUNT, name))
      expect(ttl('expandedWorkspaces')).toBe(30 * DAY_MS)
      expect(ttl('client-id')).toBe(30 * DAY_MS)
      expect(ttl('tab-mru')).toBe(30 * DAY_MS)
      expect(ttl('cli-path-checked')).toBe(1 * DAY_MS)
      expect(ttl('fileScroll:abc')).toBe(1 * DAY_MS)
      expect(ttl('activeTab:abc')).toBe(30 * DAY_MS)
      expect(ttl('tileActiveTabs:abc')).toBe(30 * DAY_MS)
      expect(ttl('focusedTile:abc')).toBe(30 * DAY_MS)
      expect(ttl('sidebar:abc')).toBe(30 * DAY_MS)
      expect(ttl('tabTree:abc')).toBe(30 * DAY_MS)
      expect(ttl('directoryTree:abc')).toBe(30 * DAY_MS)
    })
  })

  describe('initStorageCleanup', () => {
    // The first sweep is deferred off the paint path, so `App` does not walk
    // every account's keys in both stores before the first frame. jsdom has no
    // `requestIdleCallback`, so this exercises the `setTimeout(0)` fallback.
    it('sweeps once the browser is idle, not during init', () => {
      localStorage.setItem('leapmux-old-key', 'stale')
      const dispose = initStorageCleanup()
      expect(localStorage.getItem('leapmux-old-key')).toBe('stale')

      vi.advanceTimersByTime(0)
      expect(localStorage.getItem('leapmux-old-key')).toBeNull()
      dispose()
    })

    it('cancels the deferred first sweep when disposed before it runs', () => {
      localStorage.setItem('leapmux-old-key', 'stale')
      const dispose = initStorageCleanup()
      dispose()

      vi.advanceTimersByTime(60 * 60 * 1000)
      expect(localStorage.getItem('leapmux-old-key')).toBe('stale')
    })

    it('returns a dispose function that clears the interval', () => {
      const dispose = initStorageCleanup()
      // Add a stale key after the deferred init cleanup ran
      vi.advanceTimersByTime(0)
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
      const stored = storedKeyFor('ask-state:agent:req2')!

      vi.advanceTimersByTime(30 * 60 * 1000)
      localStorage.setItem(stored, JSON.stringify({ v: 'data', e: Date.now() + 1000 }))

      // Advance past expiration but not yet to the next cleanup.
      vi.advanceTimersByTime(2000)
      expect(localStorage.getItem(stored)).not.toBeNull()

      // Advance to the 1-hour mark.
      vi.advanceTimersByTime(30 * 60 * 1000 - 2000)
      expect(localStorage.getItem(stored)).toBeNull()

      dispose()
    })
  })

  // The exception, pinned. These two marks fence a process-wide sidecar relay:
  // partitioning them per account would let two accounts mint colliding ids
  // that both pass the sidecar's strictly-greater owner fence, so one process's
  // close would tear down another's relay.
  describe('the device-scoped relay marks', () => {
    it('stores with no account segment, so every account shares one sequence', () => {
      localStorageSet(KEY_CHANNEL_RELAY_SEQ, 1)
      setStorageAccount(OTHER)
      expect(storedKeyFor(KEY_CHANNEL_RELAY_SEQ)).toBe('leapmux:channel-relay-seq')
      expect(localStorage.getItem('leapmux:channel-relay-seq')).not.toBeNull()
    })
  })
})

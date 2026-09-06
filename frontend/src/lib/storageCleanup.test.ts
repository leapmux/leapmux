import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  accountStorageKey,
  flushStorageWrites,
  getSessionTtlForStoredKey,
  getTtlForKey,
  getTtlForStoredKey,
  initStorageCleanup,
  isWrappedValue,
  KEY_CHANNEL_RELAY_SEQ,
  KEY_CLIENT_ID,
  LOCAL_KEY_SPECS,
  localStorageGet,
  localStorageSet,
  mirrorEntryForTests,
  PREFIX_FILES_SHOW_HIDDEN,
  resetStorageAccountForTests,
  runCleanup,
  SESSION_KEY_SPECS,
  sessionStorageSet,
  setStorageAccountForTests,
  storedKeyFor,
} from '~/lib/browserStorage'
import { enqueueKvPut, readKvRow } from '~/lib/browserStorageDb'
import { TEST_USER_ID } from '~/test-support/crdtBridge'
import { useTestStorage } from '~/test-support/persistentStorage'

// The sweep works over IndexedDB now, so it needs a database to sweep.
useTestStorage()

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

/**
 * Put `value` at an arbitrary STORED key, bypassing the registry.
 *
 * The sweep's whole job is judging keys the accessors would refuse to compose --
 * an unregistered name, a scope that does not match its registration -- so
 * seeding them means writing at that layer.
 */
async function seedRow(storedKey: string, value: unknown, ttlMs = YEAR_MS): Promise<void> {
  enqueueKvPut({ k: storedKey, v: value, e: Date.now() + ttlMs }, { publish: false })
  await flushStorageWrites()
}

/**
 * Let an in-flight sweep finish.
 *
 * `initStorageCleanup` holds a latch so two sweeps cannot overlap, and the
 * database half of a sweep runs on REAL immediates (see the fake-timer note
 * below). So a test that fires the next tick without letting the previous sweep
 * settle finds the latch still closed and nothing happens.
 */
async function settleSweep(): Promise<void> {
  for (let i = 0; i < 20; i++)
    await new Promise(resolve => setImmediate(resolve))
}

describe('storageCleanup', () => {
  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
    // `setImmediate` stays REAL, because fake-indexeddb schedules its request
    // callbacks on it and the sweep this file tests reads a database. The fake
    // clock still owns everything the sweep schedules for itself.
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] })
  })

  afterEach(() => {
    sessionStorage.clear()
    vi.useRealTimers()
    setStorageAccountForTests(ACCOUNT)
  })

  describe('getTtlForKey', () => {
    it('returns the registered TTL for each dynamic prefix', async () => {
      // Pin the prefix/TTL pairs so a regression (wrong prefix, wrong
      // number-of-days multiplier, missing entry) is caught. Iterating the
      // table itself would only verify prefix-matching works, not that the TTL
      // values are correct.
      expect(getTtlForKey('editor-draft:abc')).toBe(7 * DAY_MS)
      expect(getTtlForKey('editor-min-height:abc')).toBe(7 * DAY_MS)
      expect(getTtlForKey('agent-session:abc')).toBe(7 * DAY_MS)
      expect(getTtlForKey('control-state:agent:req')).toBe(1 * DAY_MS)
      expect(getTtlForKey('worker-info:abc')).toBe(7 * DAY_MS)
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
    it('gives every long-lived localStorage singleton a year', async () => {
      expect(getTtlForKey('browser-prefs')).toBe(YEAR_MS)
      expect(getTtlForKey('mru-agent-providers')).toBe(YEAR_MS)
      expect(getTtlForKey('key-pins')).toBe(YEAR_MS)
      expect(getTtlForKey('directory-selector-show-hidden')).toBe(YEAR_MS)
      expect(getTtlForKey('preferred-external-app')).toBe(YEAR_MS)
      expect(getTtlForKey('user-events-relay-seq')).toBe(YEAR_MS)
      expect(getTtlForKey('channel-relay-seq')).toBe(YEAR_MS)
      // The odd one out among the templated table-mates, and deliberately so:
      // it is a preference rather than a cache, and it is the only record of
      // which workspace to reopen now that the URL carries no workspace id. A
      // day-scale TTL here would silently drop a returning user on workspace #1.
      expect(getTtlForKey('active-workspace')).toBe(YEAR_MS)
    })

    it('returns null for an unregistered name', async () => {
      expect(getTtlForKey('some-other-key')).toBeNull()
      expect(getTtlForKey('theme')).toBeNull()
      // The stored form is not a logical name.
      expect(getTtlForKey('leapmux:browser-prefs')).toBeNull()
    })
  })

  describe('isWrappedValue', () => {
    it('returns true for valid wrapped values', async () => {
      expect(isWrappedValue({ v: 'hello', e: 123 })).toBe(true)
      expect(isWrappedValue({ v: null, e: 0 })).toBe(true)
      expect(isWrappedValue({ v: { nested: true }, e: 999 })).toBe(true)
      expect(isWrappedValue({ v: 42, e: Date.now() })).toBe(true)
    })

    it('returns false for invalid values', async () => {
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
    it('resolves an account-scoped stored key for ANY account', async () => {
      expect(getTtlForStoredKey(accountStorageKey(OTHER, 'editor-draft:a'))).toBe(7 * DAY_MS)
    })

    it('resolves a device-scoped stored key', async () => {
      expect(getTtlForStoredKey('leapmux:channel-relay-seq')).toBe(YEAR_MS)
    })

    // A scope mismatch is UNKNOWN, not a fallback. That is what retires a flat
    // key left by an earlier build, and what stops a scoped copy of a device
    // key from passing as registered.
    it('refuses a key stored under the wrong scope', async () => {
      expect(getTtlForStoredKey('leapmux:browser-prefs')).toBeNull()
      expect(getTtlForStoredKey(accountStorageKey(ACCOUNT, 'channel-relay-seq'))).toBeNull()
    })

    it('refuses a malformed account segment', async () => {
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
    it('resolves an account segment holding a separator or a non-ASCII id', async () => {
      expect(getTtlForStoredKey(accountStorageKey('has-hyphen', 'browser-prefs'))).toBe(YEAR_MS)
      expect(getTtlForStoredKey(accountStorageKey('a:b', 'browser-prefs'))).toBe(YEAR_MS)
      expect(getTtlForStoredKey(accountStorageKey('사용자', 'browser-prefs'))).toBe(YEAR_MS)
      // The encoded key still holds exactly one bare separator after `u:`.
      expect(accountStorageKey('a:b', 'browser-prefs')).toBe('leapmux:u:a%3Ab:browser-prefs')
    })

    it('refuses a registered name under an unregistered one', async () => {
      expect(getTtlForStoredKey(accountStorageKey(ACCOUNT, 'not-registered'))).toBeNull()
    })

    it('resolves the session table separately from the local one', async () => {
      expect(getSessionTtlForStoredKey(accountStorageKey(ACCOUNT, 'tab-mru'))).not.toBeNull()
      // A localStorage name is not a sessionStorage name.
      expect(getSessionTtlForStoredKey(accountStorageKey(ACCOUNT, 'browser-prefs'))).toBeNull()
    })
  })

  describe('runCleanup', () => {
    it('deletes expired wrapped dynamic keys', async () => {
      localStorage.setItem(storedKeyFor('editor-draft:abc')!, JSON.stringify({ v: 'data', e: Date.now() - 1000 }))
      await runCleanup()
      expect(localStorage.getItem(storedKeyFor('editor-draft:abc')!)).toBeNull()
    })

    it('deletes unwrapped dynamic keys (old format)', async () => {
      localStorage.setItem(storedKeyFor('editor-draft:abc')!, '"raw string"')
      await runCleanup()
      expect(localStorage.getItem(storedKeyFor('editor-draft:abc')!)).toBeNull()
    })

    it('deletes old dash-convention keys', async () => {
      localStorage.setItem('leapmux-editor-draft-abc', '"raw"')
      localStorage.setItem('leapmux-theme', 'dark')
      await runCleanup()
      expect(localStorage.getItem('leapmux-editor-draft-abc')).toBeNull()
      expect(localStorage.getItem('leapmux-theme')).toBeNull()
    })

    it('deletes unrecognized leapmux: keys', async () => {
      localStorage.setItem('leapmux:some-unknown-key', '"data"')
      await runCleanup()
      expect(localStorage.getItem('leapmux:some-unknown-key')).toBeNull()
    })

    // THE RETIREMENT. The `leapmux:` family lives in IndexedDB now, so nothing
    // left in localStorage is registered any more and freshness buys it
    // nothing: it is a leftover from a build that stored it there.
    it('deletes a FRESH leapmux key left in localStorage', async () => {
      writeFresh(localStorage, storedKeyFor('files-show-hidden:abc')!)
      await runCleanup()
      expect(localStorage.getItem(storedKeyFor('files-show-hidden:abc')!)).toBeNull()
    })

    it('preserves non-leapmux keys', async () => {
      localStorage.setItem('other-app-key', 'some value')
      localStorage.setItem('random', '123')
      await runCleanup()
      expect(localStorage.getItem('other-app-key')).toBe('some value')
      expect(localStorage.getItem('random')).toBe('123')
    })

    // THE POINT OF SCOPING. A sweep that judged keys against the signed-in
    // account would wipe every other account's state on each page load, so the
    // second account on a browser would be reset by the first one opening the
    // app.
    it('preserves a fresh key belonging to a DIFFERENT account', async () => {
      setStorageAccountForTests(OTHER)
      localStorageSet('browser-prefs', { diffView: 'split' })
      await flushStorageWrites()
      setStorageAccountForTests(ACCOUNT)

      await runCleanup()

      setStorageAccountForTests(OTHER)
      expect(localStorageGet('browser-prefs')).toEqual({ diffView: 'split' })
      setStorageAccountForTests(ACCOUNT)
    })

    // Kept, but not exempt: an account nobody signs into still ages out.
    it('deletes an EXPIRED row belonging to a different account', async () => {
      setStorageAccountForTests(OTHER)
      localStorageSet('browser-prefs', { diffView: 'split' })
      await flushStorageWrites()
      setStorageAccountForTests(ACCOUNT)

      vi.advanceTimersByTime(YEAR_MS + 1)
      await runCleanup()

      setStorageAccountForTests(OTHER)
      expect(localStorageGet('browser-prefs')).toBeUndefined()
      setStorageAccountForTests(ACCOUNT)
    })

    // Unregistered, or registered under a scope the key does not carry: both
    // are unknown, and unknown goes. That is what retires a key an older build
    // wrote under a name or a scope this one no longer has.
    it('deletes an unregistered row and a wrong-scope row', async () => {
      await seedRow('leapmux:some-unknown-key', 'x')
      await seedRow(accountStorageKey(ACCOUNT, 'channel-relay-seq'), 1)
      await seedRow('leapmux:browser-prefs', 'flat copy of a scoped key')

      await runCleanup()

      expect(await readKvRow('leapmux:some-unknown-key')).toBeUndefined()
      expect(await readKvRow(accountStorageKey(ACCOUNT, 'channel-relay-seq'))).toBeUndefined()
      expect(await readKvRow('leapmux:browser-prefs')).toBeUndefined()
    })

    it('keeps a registered row that has not expired', async () => {
      localStorageSet('key-pins', { w1: 'pin' })
      await flushStorageWrites()
      await runCleanup()
      expect(await readKvRow(storedKeyFor('key-pins')!)).toBeDefined()
    })

    // The migration, such as it is: the move to scoped keys retires every flat
    // copy on the first sweep, with no migration code.
    it('deletes a flat copy of a now-scoped key, however fresh', async () => {
      writeFresh(localStorage, 'leapmux:browser-prefs', YEAR_MS)
      writeFresh(localStorage, 'leapmux:key-pins', YEAR_MS)
      writeFresh(localStorage, 'leapmux:activeWorkspace:user-1', YEAR_MS)
      await runCleanup()
      expect(localStorage.getItem('leapmux:browser-prefs')).toBeNull()
      expect(localStorage.getItem('leapmux:key-pins')).toBeNull()
      expect(localStorage.getItem('leapmux:activeWorkspace:user-1')).toBeNull()
    })

    it('deletes a scoped copy of a device-scoped key', async () => {
      writeFresh(localStorage, accountStorageKey(ACCOUNT, 'channel-relay-seq'), YEAR_MS)
      await runCleanup()
      expect(localStorage.getItem(accountStorageKey(ACCOUNT, 'channel-relay-seq'))).toBeNull()
    })

    it('deletes a malformed account segment', async () => {
      for (const stored of ['leapmux:u:', 'leapmux:u:abc', 'leapmux:u::browser-prefs'])
        writeFresh(localStorage, stored, YEAR_MS)
      await runCleanup()
      for (const stored of ['leapmux:u:', 'leapmux:u:abc', 'leapmux:u::browser-prefs'])
        expect(localStorage.getItem(stored), stored).toBeNull()
    })

    // The sweep runs from `app.tsx` before any provider mounts and before the
    // auth bootstrap answers, so it is the one storage caller that must work
    // with no account at all.
    it('runs with no storage account set, and keeps the device keys', async () => {
      localStorageSet('channel-relay-seq', 7)
      await flushStorageWrites()

      resetStorageAccountForTests()
      await expect(runCleanup()).resolves.toBeUndefined()

      expect(await readKvRow('leapmux:channel-relay-seq')).toBeDefined()
      setStorageAccountForTests(ACCOUNT)
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
    // The mirror is the synchronous tier's whole answer, so a row the sweep
    // deleted has to leave it too -- otherwise `localStorageGet` keeps serving a
    // value that is no longer anywhere, for as long as the page lives. And the
    // other tabs mirror the same row, so they are told.
    it('drops what it deleted from the mirror and tells the other tabs', async () => {
      const name = `${PREFIX_FILES_SHOW_HIDDEN}w1:/repo` as const
      localStorageSet(name, true)
      await flushStorageWrites()
      const stored = storedKeyFor(name)!
      expect(mirrorEntryForTests(stored)).toBeDefined()

      const published = vi.spyOn(BroadcastChannel.prototype, 'postMessage')
      // Past the family's 7-day TTL, so the sweep's expiry arm selects it.
      vi.advanceTimersByTime(8 * DAY_MS)
      await runCleanup()

      expect(await readKvRow(stored)).toBeUndefined()
      expect(mirrorEntryForTests(stored)).toBeUndefined()
      expect(localStorageGet(name)).toBeUndefined()
      expect(published).toHaveBeenCalledWith(expect.objectContaining({
        changes: [{ k: stored, removed: true }],
      }))
      published.mockRestore()
    })

    it('preserves every registered sessionStorage key under runCleanup', async () => {
      const names = Object.entries(SESSION_KEY_SPECS)
        .map(([name, spec]) => (spec.match === 'prefix' ? `${name}sample` : name))
      for (const name of names)
        sessionStorageSet(name, 'sample')

      await runCleanup()

      expect(names.length).toBeGreaterThan(0)
      for (const name of names)
        expect(sessionStorage.getItem(storedKeyFor(name)!), name).not.toBeNull()
    })

    it('preserves every registered localStorage key under runCleanup', async () => {
      const names = Object.entries(LOCAL_KEY_SPECS)
        .map(([name, spec]) => (spec.match === 'prefix' ? `${name}sample` : name))
      for (const name of names)
        await seedRow(storedKeyFor(name)!, 'sample')

      await runCleanup()

      expect(names.length).toBeGreaterThan(0)
      for (const name of names)
        expect(await readKvRow(storedKeyFor(name)!), name).toBeDefined()
    })

    it('deletes unwrapped copies of every registered localStorage key', async () => {
      const names = Object.keys(LOCAL_KEY_SPECS).filter(n => !n.endsWith(':'))
      for (const name of names)
        localStorage.setItem(storedKeyFor(name)!, '"raw-legacy-value"')
      await runCleanup()
      for (const name of names)
        expect(localStorage.getItem(storedKeyFor(name)!), name).toBeNull()
    })

    // Singleton sessionStorage keys are matched by exact string. A neighbour
    // whose name starts with the singleton must NOT inherit its TTL via prefix
    // matching — that is the whole reason the exact match exists.
    it('does not bleed exact-match TTLs into prefix-matched neighbours', async () => {
      expect(getSessionTtlForStoredKey(accountStorageKey(ACCOUNT, KEY_CLIENT_ID))).not.toBeNull()
      expect(getSessionTtlForStoredKey(accountStorageKey(ACCOUNT, `${KEY_CLIENT_ID}-extra`))).toBeNull()
      expect(getSessionTtlForStoredKey(accountStorageKey(ACCOUNT, `${KEY_CLIENT_ID}:foo`))).toBeNull()
    })

    // Every session singleton, pinned by value for the same reason the
    // localStorage ones are: iterating the table proves only that the lookup
    // works.
    it('gives every sessionStorage singleton its registered TTL', async () => {
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
    it('sweeps once the browser is idle, not during init', async () => {
      localStorage.setItem('leapmux-old-key', 'stale')
      const dispose = initStorageCleanup()
      expect(localStorage.getItem('leapmux-old-key')).toBe('stale')

      vi.advanceTimersByTime(0)
      expect(localStorage.getItem('leapmux-old-key')).toBeNull()
      dispose()
    })

    it('cancels the deferred first sweep when disposed before it runs', async () => {
      localStorage.setItem('leapmux-old-key', 'stale')
      const dispose = initStorageCleanup()
      dispose()

      vi.advanceTimersByTime(60 * 60 * 1000)
      expect(localStorage.getItem('leapmux-old-key')).toBe('stale')
    })

    it('returns a dispose function that clears the interval', async () => {
      const dispose = initStorageCleanup()
      // Add a stale key after the deferred init cleanup ran.
      await vi.advanceTimersByTimeAsync(0)
      await settleSweep()
      localStorage.setItem('leapmux-stale', 'data')

      // Advance time by 1 hour — should trigger cleanup.
      await vi.advanceTimersByTimeAsync(60 * 60 * 1000)
      expect(localStorage.getItem('leapmux-stale')).toBeNull()

      // After dispose, cleanup should not run.
      await settleSweep()
      localStorage.setItem('leapmux-stale2', 'data')
      dispose()
      await vi.advanceTimersByTimeAsync(60 * 60 * 1000)
      expect(localStorage.getItem('leapmux-stale2')).toBe('data')
    })

    // The latch. A sweep that is still reading the database has already walked
    // every key the next one would, so a second pass is pure duplicate work --
    // and the two would each report the other's deletions as their own.
    it('does not start a second sweep while the first is still running', async () => {
      const dispose = initStorageCleanup()

      // Start the deferred first sweep and deliberately DO NOT settle it: its
      // synchronous halves have run and its database half is still in flight,
      // which is exactly the window the latch covers.
      vi.advanceTimersByTime(0)

      // Only the synchronous legacy pass deletes this, so its survival is proof
      // that the hourly tick found the latch closed and did nothing.
      localStorage.setItem('leapmux-old-key', 'stale')
      vi.advanceTimersByTime(60 * 60 * 1000)
      expect(localStorage.getItem('leapmux-old-key')).toBe('stale')

      // Once the first sweep settles, the latch reopens and the next tick runs.
      await settleSweep()
      await vi.advanceTimersByTimeAsync(60 * 60 * 1000)
      expect(localStorage.getItem('leapmux-old-key')).toBeNull()
      dispose()
    })

    it('sets up hourly interval', async () => {
      const dispose = initStorageCleanup()
      const stored = storedKeyFor('control-state:agent:req2')!

      await vi.advanceTimersByTimeAsync(30 * 60 * 1000)
      await settleSweep()
      await seedRow(stored, 'data', 1000)

      // Advance past expiration but not yet to the next cleanup.
      await vi.advanceTimersByTimeAsync(2000)
      expect(await readKvRow(stored)).toBeDefined()

      // Advance to the 1-hour mark.
      await vi.advanceTimersByTimeAsync(30 * 60 * 1000 - 2000)
      expect(await readKvRow(stored)).toBeUndefined()

      dispose()
    })
  })

  // The exception, pinned. These two marks fence a process-wide sidecar relay:
  // partitioning them per account would let two accounts mint colliding ids
  // that both pass the sidecar's strictly-greater owner fence, so one process's
  // close would tear down another's relay.
  describe('the device-scoped relay marks', () => {
    it('stores with no account segment, so every account shares one sequence', async () => {
      localStorageSet(KEY_CHANNEL_RELAY_SEQ, 1)
      setStorageAccountForTests(OTHER)
      expect(storedKeyFor(KEY_CHANNEL_RELAY_SEQ)).toBe('leapmux:channel-relay-seq')
      // The other account reads the SAME mark, which is the whole point of the
      // device scope: one sidecar, one sequence.
      expect(localStorageGet(KEY_CHANNEL_RELAY_SEQ)).toBe(1)
    })
  })
})

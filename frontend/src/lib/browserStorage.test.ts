import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  accountStorageKey,
  accountStorageKeyPrefix,
  batchBrowserPrefWrites,
  hasStorageAccount,
  KEY_BROWSER_PREFS,
  KEY_CHANNEL_RELAY_SEQ,
  KEY_CLIENT_ID,
  LOCAL_KEY_SPECS,
  localStorageGet,
  localStorageRemove,
  localStorageSet,
  onStorageAccountChange,
  resetStorageAccountForTests,
  SESSION_KEY_SPECS,
  sessionStorageClearForTests,
  sessionStorageGet,
  sessionStorageHas,
  sessionStorageRemove,
  sessionStorageSet,
  setStorageAccount,
  storedKeyFor,
} from '~/lib/browserStorage'
import { TEST_USER_ID } from '~/test-support/crdtBridge'

// Restated rather than imported, so an assertion is an INDEPENDENT statement of
// the number the registry holds.
const DAY_MS = 24 * 60 * 60 * 1000
const HOUR_MS = 60 * 60 * 1000
const YEAR_MS = 365 * DAY_MS

// The account `vitest.setup.ts` signs the suite in as. Taken from there rather
// than spelled again, because it is not an expectation of this file's own -- it
// is the identity every read and write here resolves under.
const ACCOUNT = TEST_USER_ID
const OTHER = 'otheraccount'

/** The stored key `name` lands on for `ACCOUNT`. */
function key(name: string): string {
  return accountStorageKey(ACCOUNT, name)
}

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
        localStorageSet('editor-draft:abc', { content: 'hello', cursor: 5 })
        const raw = localStorage.getItem(key('editor-draft:abc'))
        expect(raw).not.toBeNull()
        const parsed = JSON.parse(raw!)
        expect(parsed.v).toEqual({ content: 'hello', cursor: 5 })
        expect(parsed.e).toBe(Date.now() + 7 * DAY_MS)
      })

      it('uses correct TTL per prefix', () => {
        localStorageSet('control-state:a:r', { selections: {} })
        const parsed = JSON.parse(localStorage.getItem(key('control-state:a:r'))!)
        expect(parsed.e).toBe(Date.now() + 1 * DAY_MS)
      })

      it('uses correct TTL for each dynamic prefix', () => {
        for (const [name, spec] of Object.entries(LOCAL_KEY_SPECS)) {
          if (spec.match !== 'prefix')
            continue
          localStorageSet(`${name}test-id`, 'test')
          const parsed = JSON.parse(localStorage.getItem(key(`${name}test-id`))!)
          expect(parsed.e, name).toBe(Date.now() + spec.ttlMs)
        }
      })
    })

    describe('exact keys', () => {
      it('wraps with 1-year TTL', () => {
        localStorageSet('mru-agent-providers', ['claude', 'codex'])
        const parsed = JSON.parse(localStorage.getItem(key('mru-agent-providers'))!)
        expect(parsed.v).toEqual(['claude', 'codex'])
        expect(parsed.e).toBe(Date.now() + YEAR_MS)
      })

      it('uses its registered TTL for every exact key', () => {
        for (const [name, spec] of Object.entries(LOCAL_KEY_SPECS)) {
          if (spec.match !== 'exact')
            continue
          localStorageSet(name, 'test')
          const parsed = JSON.parse(localStorage.getItem(storedKeyFor(name)!)!)
          expect(parsed.e, name).toBe(Date.now() + spec.ttlMs)
        }
      })
    })

    describe('unrecognized keys', () => {
      it('throws for an unregistered name', () => {
        expect(() => localStorageSet('unknown-key', 'val')).toThrow(/Unknown localStorage key/)
      })

      // The stored form is this module's to compose. A caller that passes one
      // is stating a layout it does not own, and the name it spells cannot be
      // registered, so it must not quietly resolve to anything.
      it('throws for a name that spells out the stored form', () => {
        expect(() => localStorageSet('leapmux:browser-prefs', 'val')).toThrow(/Unknown localStorage key/)
      })
    })
  })

  describe('localStorageGet', () => {
    it('returns v from a valid wrapped value', () => {
      localStorageSet('editor-draft:abc', { content: 'hello' })
      expect(localStorageGet<{ content: string }>('editor-draft:abc')).toEqual({ content: 'hello' })
    })

    it('returns undefined for unwrapped value (old format)', () => {
      localStorage.setItem(key('editor-draft:abc'), JSON.stringify({ content: 'old' }))
      expect(localStorageGet('editor-draft:abc')).toBeUndefined()
    })

    it('returns undefined and deletes key when expired', () => {
      localStorage.setItem(key('editor-draft:abc'), JSON.stringify({ v: { data: true }, e: Date.now() - 1 }))
      expect(localStorageGet('editor-draft:abc')).toBeUndefined()
      expect(localStorage.getItem(key('editor-draft:abc'))).toBeNull()
    })

    it('refreshes expiration on read when 3+ hours have passed', () => {
      localStorageSet('editor-draft:abc', 'data')
      const originalExpiry = JSON.parse(localStorage.getItem(key('editor-draft:abc'))!).e

      vi.advanceTimersByTime(4 * HOUR_MS)

      localStorageGet('editor-draft:abc')
      const rawAfter = JSON.parse(localStorage.getItem(key('editor-draft:abc'))!)
      expect(rawAfter.e).toBe(Date.now() + 7 * DAY_MS)
      expect(rawAfter.e).toBeGreaterThan(originalExpiry)
    })

    it('does NOT refresh expiration when < 3 hours have passed', () => {
      localStorageSet('editor-draft:abc', 'data')
      const originalExpiry = JSON.parse(localStorage.getItem(key('editor-draft:abc'))!).e

      vi.advanceTimersByTime(2 * HOUR_MS)

      localStorageGet('editor-draft:abc')
      expect(JSON.parse(localStorage.getItem(key('editor-draft:abc'))!).e).toBe(originalExpiry)
    })

    it('returns undefined for missing key', () => {
      expect(localStorageGet('editor-draft:nonexistent')).toBeUndefined()
    })

    it('refreshes expiration on read across a long inactivity gap', () => {
      // The 1-year TTL plus refresh-on-read is the "never expires while
      // actively used" contract for preferences/trust state. After a
      // 6-month idle window, the next read should push expiration back
      // out to a year from now.
      localStorageSet('preferred-external-app', 'vscode')
      vi.advanceTimersByTime(180 * DAY_MS)
      localStorageGet('preferred-external-app')
      expect(JSON.parse(localStorage.getItem(key('preferred-external-app'))!).e).toBe(Date.now() + YEAR_MS)
    })

    it('throws for an unregistered name', () => {
      expect(() => localStorageGet('unknown')).toThrow(/Unknown localStorage key/)
    })
  })

  describe('localStorageRemove', () => {
    it('removes the key', () => {
      localStorageSet('key-pins', { w: 'pin' })
      localStorageRemove('key-pins')
      expect(localStorage.getItem(key('key-pins'))).toBeNull()
    })

    it('does not throw for a registered but absent key', () => {
      expect(() => localStorageRemove('key-pins')).not.toThrow()
    })

    // It used to skip validation entirely. It cannot any more: composing the
    // stored key REQUIRES the registration that says which namespace the name
    // lives in, so an unregistered name has no key to remove and a silent
    // no-op would tell the caller it deleted something.
    it('throws for an unregistered name rather than removing nothing', () => {
      expect(() => localStorageRemove('unregistered')).toThrow(/Unknown localStorage key/)
    })
  })

  describe('the account namespace', () => {
    it('composes an account-scoped key under the signed-in account', () => {
      localStorageSet(KEY_BROWSER_PREFS, { diffView: 'split' })
      expect(localStorage.getItem(`leapmux:u:${ACCOUNT}:browser-prefs`)).not.toBeNull()
      expect(storedKeyFor(KEY_BROWSER_PREFS)).toBe(`leapmux:u:${ACCOUNT}:browser-prefs`)
    })

    it('composes a device-scoped key with no account segment', () => {
      localStorageSet(KEY_CHANNEL_RELAY_SEQ, 7)
      expect(localStorage.getItem('leapmux:channel-relay-seq')).not.toBeNull()
      expect(storedKeyFor(KEY_CHANNEL_RELAY_SEQ)).toBe('leapmux:channel-relay-seq')
    })

    describe('with no account set', () => {
      beforeEach(() => {
        resetStorageAccountForTests()
      })

      afterEach(() => {
        setStorageAccount(ACCOUNT)
      })

      it('reports that no account is available', () => {
        expect(hasStorageAccount()).toBe(false)
      })

      // The whole point: a value written before the identity resolves has no
      // correct owner, so there is no safe fallback to take.
      it('throws from every account-scoped accessor', () => {
        expect(() => localStorageGet(KEY_BROWSER_PREFS)).toThrow(/No storage account is set/)
        expect(() => localStorageSet(KEY_BROWSER_PREFS, {})).toThrow(/No storage account is set/)
        expect(() => localStorageRemove(KEY_BROWSER_PREFS)).toThrow(/No storage account is set/)
        expect(() => sessionStorageGet('tab-mru')).toThrow(/No storage account is set/)
        expect(() => sessionStorageSet('tab-mru', {})).toThrow(/No storage account is set/)
        expect(() => sessionStorageHas('tab-mru')).toThrow(/No storage account is set/)
        expect(() => sessionStorageRemove('tab-mru')).toThrow(/No storage account is set/)
      })

      // The relay marks fence a process-wide sidecar, and the desktop allocates
      // one before anybody signs in. That is the whole reason they are device
      // scoped, so it gets a direct test.
      it('still reads and writes a device-scoped key', () => {
        localStorageSet(KEY_CHANNEL_RELAY_SEQ, 42)
        expect(localStorageGet<number>(KEY_CHANNEL_RELAY_SEQ)).toBe(42)
      })

      it('answers null for an account-scoped stored key, matching no event', () => {
        expect(storedKeyFor(KEY_BROWSER_PREFS)).toBeNull()
        expect(storedKeyFor(KEY_CHANNEL_RELAY_SEQ)).toBe('leapmux:channel-relay-seq')
      })
    })

    // `storedKeyFor` answers null for a legitimate state -- no account yet. An
    // unregistered name is a programming error, and it throws like everywhere
    // else in this module. Answering null for both would make a misspelled name
    // indistinguishable from "no account" at the one caller, so cross-tab
    // preference sync would silently stop following the other tab -- which is
    // the exact failure that listener already shipped with once.
    it('throws for an unregistered name rather than answering null', () => {
      expect(() => storedKeyFor('not-registered')).toThrow(/Unknown storage key/)
      // Even with no account, where a null answer would look plausible.
      resetStorageAccountForTests()
      expect(() => storedKeyFor('not-registered')).toThrow(/Unknown storage key/)
      setStorageAccount(ACCOUNT)
    })

    it('states one account prefix that a caller can match a whole namespace with', () => {
      expect(accountStorageKeyPrefix(ACCOUNT)).toBe(`leapmux:u:${ACCOUNT}:`)
      expect(accountStorageKey(ACCOUNT, KEY_BROWSER_PREFS).startsWith(accountStorageKeyPrefix(ACCOUNT))).toBe(true)
    })

    // SESSIONSTORAGE IS SCOPED TOO, and it carries the one key whose registration
    // states a correctness consequence rather than a preference: `checkpointStore`
    // keys its records by `[userId, clientId]`, so a second account resuming the
    // first account's client id claims checkpoints that are not its own -- silent,
    // permanent CRDT divergence. A tab outlives a sign-out, so a second account
    // signing in to the SAME tab is the reachable case.
    it('gives each account its own sessionStorage namespace', () => {
      sessionStorageSet(KEY_CLIENT_ID, 'client-alpha')

      setStorageAccount(OTHER)
      expect(sessionStorageGet(KEY_CLIENT_ID)).toBeUndefined()
      expect(sessionStorageHas(KEY_CLIENT_ID)).toBe(false)
      sessionStorageSet(KEY_CLIENT_ID, 'client-beta')

      setStorageAccount(ACCOUNT)
      expect(sessionStorageGet(KEY_CLIENT_ID)).toBe('client-alpha')
      expect(sessionStorage.getItem(accountStorageKey(OTHER, KEY_CLIENT_ID))).not.toBeNull()
    })

    // The two tables are consulted independently, so a name registered for one
    // store must not resolve against the other's index.
    it('resolves a sessionStorage name against the session table alone', () => {
      expect(storedKeyFor(KEY_CLIENT_ID)).toBe(accountStorageKey(ACCOUNT, KEY_CLIENT_ID))
      expect(() => sessionStorageGet(KEY_BROWSER_PREFS)).toThrow(/Unknown sessionStorage key/)
      expect(() => localStorageGet(KEY_CLIENT_ID)).toThrow(/Unknown localStorage key/)
    })

    // A cache that mirrors an account-scoped key in memory has to move with the
    // namespace. It subscribes here rather than being invalidated by hand at
    // each site, and the notification is SYNCHRONOUS with the move: a
    // subscriber that read on the next tick could serve the previous account's
    // copy to a render that already saw the new identity.
    describe('onStorageAccountChange', () => {
      it('notifies synchronously, after the namespace has moved', () => {
        const seen: Array<string | null> = []
        const off = onStorageAccountChange(() => seen.push(storedKeyFor(KEY_BROWSER_PREFS)))

        setStorageAccount(OTHER)
        expect(seen).toEqual([accountStorageKey(OTHER, KEY_BROWSER_PREFS)])

        off()
        setStorageAccount(ACCOUNT)
        expect(seen).toHaveLength(1)
      })

      it('stays quiet for an unchanged id', () => {
        const listener = vi.fn()
        const off = onStorageAccountChange(listener)
        setStorageAccount(ACCOUNT)
        expect(listener).not.toHaveBeenCalled()
        off()
      })

      it('notifies the first account, so a subscriber needs no separate seed', () => {
        resetStorageAccountForTests()
        const listener = vi.fn()
        onStorageAccountChange(listener)
        setStorageAccount(ACCOUNT)
        expect(listener).toHaveBeenCalledTimes(1)
      })
    })

    it('leaves the previous account untouched when the namespace moves', () => {
      localStorageSet(KEY_BROWSER_PREFS, { diffView: 'split' })

      setStorageAccount(OTHER)
      expect(localStorageGet(KEY_BROWSER_PREFS)).toBeUndefined()
      localStorageSet(KEY_BROWSER_PREFS, { diffView: 'unified' })

      setStorageAccount(ACCOUNT)
      expect(localStorageGet(KEY_BROWSER_PREFS)).toEqual({ diffView: 'split' })
      expect(JSON.parse(localStorage.getItem(accountStorageKey(OTHER, KEY_BROWSER_PREFS))!).v)
        .toEqual({ diffView: 'unified' })
    })

    it('refuses an empty id, which names no account', () => {
      expect(() => setStorageAccount('')).toThrow(/Invalid storage account id/)
    })

    // The stored key splits on the first ':' after `u:`, and the id segment is
    // percent-encoded, so an id holding a separator stays unambiguous. Real ids
    // are nanoids over [A-Za-z0-9], which encoding leaves byte-identical -- the
    // encoding is there so the day the hub's format widens is not the day
    // sign-in throws.
    it('carries any id the hub can mint, separators included', () => {
      for (const id of ['has:colon', 'has-hyphen', 'has%percent', '사용자']) {
        setStorageAccount(id)
        localStorageSet(KEY_BROWSER_PREFS, { diffView: 'split' })
        expect(localStorageGet(KEY_BROWSER_PREFS), id).toEqual({ diffView: 'split' })
        // Exactly one bare separator after `u:`, so the parse back is exact.
        const stored = storedKeyFor(KEY_BROWSER_PREFS)!
        expect(stored.slice('leapmux:u:'.length).split(':'), id).toHaveLength(2)
      }
    })

    it('leaves an alphanumeric id byte-identical in the stored key', () => {
      expect(accountStorageKey('AbC123', KEY_BROWSER_PREFS)).toBe('leapmux:u:AbC123:browser-prefs')
    })

    it('is a no-op for an unchanged id', () => {
      localStorageSet(KEY_BROWSER_PREFS, { diffView: 'split' })
      setStorageAccount(ACCOUNT)
      expect(localStorageGet(KEY_BROWSER_PREFS)).toEqual({ diffView: 'split' })
    })

    // A batch holds ONE account's document in memory and stores it in a
    // `finally`. Moving the namespace underneath it would write the outgoing
    // account's whole document into the incoming account's key.
    it('refuses to move while a browser-preference batch is open', () => {
      let threw: unknown
      batchBrowserPrefWrites(() => {
        try {
          setStorageAccount(OTHER)
        }
        catch (err) {
          threw = err
        }
      })
      expect(String(threw)).toMatch(/browser-preference batch is open/)
      // The move was refused, so the namespace is unchanged.
      expect(storedKeyFor(KEY_BROWSER_PREFS)).toBe(`leapmux:u:${ACCOUNT}:browser-prefs`)
    })
  })

  describe('the key registry', () => {
    const every = [
      ...Object.entries(LOCAL_KEY_SPECS),
      ...Object.entries(SESSION_KEY_SPECS),
    ]

    // A regex or a table that matched nothing would make every assertion below
    // pass for the wrong reason.
    it('registers keys in both stores', () => {
      expect(Object.keys(LOCAL_KEY_SPECS).length).toBeGreaterThan(0)
      expect(Object.keys(SESSION_KEY_SPECS).length).toBeGreaterThan(0)
    })

    it('names keys logically, never in their stored form', () => {
      for (const [name] of every) {
        expect(name, name).not.toContain('leapmux')
        expect(name.startsWith('u:'), name).toBe(false)
        expect(name, name).not.toBe('')
      }
    })

    it('ends every prefix name with a separator, and no exact name', () => {
      for (const [name, spec] of every)
        expect(name.endsWith(':'), name).toBe(spec.match === 'prefix')
    })

    // The exact/prefix split exists so a singleton cannot inherit a TTL from a
    // prefix that happens to be its leading substring. That only holds while no
    // exact name starts with a prefix name.
    it('keeps no exact name under a prefix name', () => {
      for (const table of [LOCAL_KEY_SPECS, SESSION_KEY_SPECS]) {
        const prefixes = Object.entries(table).filter(([, s]) => s.match === 'prefix').map(([n]) => n)
        for (const [name, spec] of Object.entries(table)) {
          if (spec.match !== 'exact')
            continue
          for (const prefix of prefixes)
            expect(name.startsWith(prefix), `${name} shadowed by ${prefix}`).toBe(false)
        }
      }
    })

    // And no prefix under another prefix, which is the same rule from the other
    // side. `specFor` returns the FIRST prefix that matches, scanning in
    // declaration order, so registering both `foo:` and `foo:bar:` would give
    // `foo:bar:x` whichever TTL AND SCOPE happened to be declared first --
    // silently, and differently again if the two entries were ever reordered.
    it('keeps no prefix name under another prefix name', () => {
      for (const table of [LOCAL_KEY_SPECS, SESSION_KEY_SPECS]) {
        const prefixes = Object.entries(table).filter(([, s]) => s.match === 'prefix').map(([n]) => n)
        for (const name of prefixes) {
          for (const other of prefixes) {
            if (other === name)
              continue
            expect(name.startsWith(other), `${name} shadowed by ${other}`).toBe(false)
          }
        }
      }
    })

    // Device scope is the exception, and it is meant to stay two keys wide.
    // A third one arriving silently is the way this erodes.
    it('scopes everything to the account except the two relay marks', () => {
      const device = every.filter(([, spec]) => spec.scope === 'device').map(([name]) => name)
      expect(device.sort()).toEqual(['channel-relay-seq', 'user-events-relay-seq'])
    })
  })

  // A refused write has nowhere to go -- a draft, a layout snapshot or a key pin
  // cannot be retried anywhere else -- so it stays swallowed. It must not be
  // SILENT: every key is partitioned per account, so the origin quota is the
  // usual cause, and the symptom a user reports is "my preferences stop saving".
  describe('a write the browser refuses', () => {
    afterEach(() => {
      vi.restoreAllMocks()
    })

    // The refresh-on-read write is a best-effort extension of a value that is
    // already read and parsed. Letting it fail the READ would revert every
    // device preference to its default while the document sat intact on disk.
    it('does not discard a value whose TTL refresh was refused', () => {
      localStorageSet(KEY_BROWSER_PREFS, { diffView: 'split' })
      // Past the refresh threshold, so the next read tries to re-stamp it.
      vi.advanceTimersByTime(YEAR_MS - HOUR_MS)
      vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
        throw new DOMException('quota', 'QuotaExceededError')
      })

      expect(localStorageGet(KEY_BROWSER_PREFS)).toEqual({ diffView: 'split' })
    })

    it('is logged rather than thrown, for both stores', () => {
      const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
      vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
        throw new DOMException('quota', 'QuotaExceededError')
      })

      expect(() => localStorageSet(KEY_BROWSER_PREFS, { diffView: 'split' })).not.toThrow()
      expect(() => sessionStorageSet('tab-mru', {})).not.toThrow()

      expect(warn).toHaveBeenCalledTimes(2)
      expect(String(warn.mock.calls[0])).toMatch(/localStorage write failed for "browser-prefs"/)
      expect(String(warn.mock.calls[1])).toMatch(/sessionStorage write failed for "tab-mru"/)
    })

    // An absent global is not a refusal. Node has neither store -- server-side
    // rendering, and the E2E harness that drives the channel code outside a
    // browser -- and warning there would put a line on the console for every
    // write, which is how a real refusal stops being noticed.
    it('stays quiet when the environment has no storage at all', () => {
      const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
      vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
        throw new ReferenceError('localStorage is not defined')
      })

      expect(() => localStorageSet(KEY_BROWSER_PREFS, {})).not.toThrow()
      expect(warn).not.toHaveBeenCalled()
    })
  })

  // Both stores forward a wholesale clear, so a fixture never reaches around
  // this module for the half it does not offer.
  describe('the test-only clears', () => {
    it('clears sessionStorage through the module', () => {
      sessionStorageSet('tab-mru', { a: 1 })
      expect(sessionStorageHas('tab-mru')).toBe(true)
      sessionStorageClearForTests()
      expect(sessionStorageHas('tab-mru')).toBe(false)
    })
  })
})

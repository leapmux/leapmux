import type { IdbSchema } from './idb'
import { IDBFactory, IDBKeyRange } from 'fake-indexeddb'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  createIdbConnection,
  forEachCursor,
  forEachCursorWhile,
  isIndexedDbAvailable,
  requestToPromise,
  selectSweepVictims,
} from './idb'

beforeEach(() => {
  vi.stubGlobal('indexedDB', new IDBFactory())
  vi.stubGlobal('IDBKeyRange', IDBKeyRange)
})
afterEach(() => {
  vi.unstubAllGlobals()
})

const STORE = 'rows'

const SCHEMA: IdbSchema = { [STORE]: { keyPath: 'k' } }

/** Open `name` at `version` with the baseline single-store schema. */
function connect(name: string, version = 1, schema: IdbSchema = SCHEMA) {
  return createIdbConnection(name, version, schema)
}

async function putRow(conn: ReturnType<typeof connect>, k: string): Promise<void> {
  const db = await conn.open()
  await new Promise<void>((resolve, reject) => {
    const tx = db.transaction(STORE, 'readwrite')
    tx.objectStore(STORE).put({ k })
    tx.oncomplete = () => resolve()
    tx.onerror = () => reject(tx.error)
  })
}

async function countRows(conn: ReturnType<typeof connect>): Promise<number> {
  const db = await conn.open()
  return requestToPromise(db.transaction(STORE, 'readonly').objectStore(STORE).count())
}

describe('createIdbConnection', () => {
  it('creates the schema on a first open', async () => {
    const conn = connect('fresh', 1)
    const db = await conn.open()
    expect(db.objectStoreNames.contains(STORE)).toBe(true)
    conn.reset()
  })

  it('caches the connection across calls', async () => {
    const conn = connect('cached', 1)
    const [a, b] = await Promise.all([conn.open(), conn.open()])
    expect(a).toBe(b)
    conn.reset()
  })

  // A version bump REBUILDS rather than migrates. There is no upgrade callback
  // to hand a versionchange transaction to, because there is no migration to
  // write: these databases are caches over state the hub owns, so the shape is
  // rebuilt from the declaration and the rows are simply gone. See ~/lib/idb's
  // header.
  it('rebuilds the stores on a version bump instead of migrating them', async () => {
    const v1 = connect('bumped', 1)
    await putRow(v1, 'stale-a')
    await putRow(v1, 'stale-b')
    expect(await countRows(v1)).toBe(2)
    // Release the v1 handle; a held connection would block the version change.
    ;(await v1.open()).close()
    v1.reset()

    const v2 = connect('bumped', 2)
    await expect(v2.open()).resolves.toBeDefined()
    expect(await countRows(v2)).toBe(0)
    v2.reset()
  })

  it('drops the cached promise on a failed open so a later call retries', async () => {
    // An unusable key path makes createObjectStore throw inside the upgrade,
    // which aborts it and fails the open.
    const openSpy = vi.spyOn(indexedDB, 'open')
    const conn = connect('upgrade-throws', 1, { [STORE]: { keyPath: '!!not a key path' } })

    await expect(conn.open()).rejects.toBeDefined()
    // A retry must produce a fresh request rather than replay the cached
    // rejection forever; it fails again for the same reason, but it is a NEW
    // attempt, which is what lets a transient failure (quota, blocked) recover.
    await expect(conn.open()).rejects.toBeDefined()
    expect(openSpy).toHaveBeenCalledTimes(2)
    openSpy.mockRestore()
    conn.reset()
  })

  // IndexedDB has no downgrade: opening below the stored version fails the
  // request with a VersionError. Left alone, that silently and PERMANENTLY
  // disables persistence for the profile, because every call site swallows.
  // It is routine, not exotic -- a deploy rollback, a stale cached bundle, or
  // an older desktop build launched after a newer one all produce it.
  describe('when the stored database is NEWER than this build', () => {
    it('recreates it rather than failing forever', async () => {
      const v2 = connect('newer', 2)
      await putRow(v2, 'from-the-newer-build')
      ;(await v2.open()).close()
      v2.reset()

      // The older build opens successfully...
      const v1 = connect('newer', 1)
      const db = await v1.open()
      expect(db.version).toBe(1)
      // ...against a fresh database. Both stores behind this scaffold are pure
      // caches over rebuildable data, so discarding the newer schema costs a
      // cold start and nothing else.
      expect(await countRows(v1)).toBe(0)
      v1.reset()
    })

    it('leaves the database alone on a non-version failure', async () => {
      // Only a VersionError recreates, and only a schema MISMATCH rebuilds. A
      // transient open failure -- quota, a browser refusing the request -- must
      // not be an excuse to destroy the cache.
      const seeded = connect('non-version', 1)
      await putRow(seeded, 'keep-me')
      ;(await seeded.open()).close()
      seeded.reset()

      const openSpy = vi.spyOn(indexedDB, 'open').mockImplementation(() => {
        const request = {
          error: new DOMException('simulated quota failure', 'QuotaExceededError'),
        } as unknown as IDBOpenDBRequest & { onerror?: () => void }
        queueMicrotask(() => request.onerror?.())
        return request
      })
      const failing = connect('non-version', 1)
      await expect(failing.open()).rejects.toBeDefined()
      openSpy.mockRestore()
      failing.reset()

      const reader = connect('non-version', 1)
      expect(await countRows(reader)).toBe(1)
      reader.reset()
    })
  })
})

describe('forEachCursor', () => {
  async function seed(conn: ReturnType<typeof connect>, keys: string[]): Promise<void> {
    for (const k of keys)
      await putRow(conn, k)
  }

  it('visits every row in key order', async () => {
    const conn = connect('walk', 1)
    await seed(conn, ['c', 'a', 'b'])
    const db = await conn.open()

    const seen: string[] = []
    const tx = db.transaction(STORE, 'readonly')
    await forEachCursor(tx.objectStore(STORE).openCursor(), (cursor) => {
      seen.push((cursor.value as { k: string }).k)
    })
    expect(seen).toEqual(['a', 'b', 'c'])
    conn.reset()
  })

  it('resolves immediately on an empty range', async () => {
    const conn = connect('walk-empty', 1)
    const db = await conn.open()
    const tx = db.transaction(STORE, 'readonly')
    let visits = 0
    await forEachCursor(tx.objectStore(STORE).openCursor(), () => {
      visits++
    })
    expect(visits).toBe(0)
    conn.reset()
  })

  it('supports a delete-while-walking key cursor', async () => {
    // The shape deleteOpLogRange uses: openKeyCursor + delete by primary key.
    const conn = connect('walk-delete', 1)
    await seed(conn, ['a', 'b', 'c'])
    const db = await conn.open()

    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction(STORE, 'readwrite')
      const store = tx.objectStore(STORE)
      void forEachCursor(store.openKeyCursor(), (cursor) => {
        store.delete(cursor.primaryKey)
      })
      tx.oncomplete = () => resolve()
      tx.onerror = () => reject(tx.error)
    })

    expect(await countRows(conn)).toBe(0)
    conn.reset()
  })
})

describe('forEachCursorWhile', () => {
  async function seed(conn: ReturnType<typeof connect>, keys: string[]): Promise<void> {
    for (const k of keys)
      await putRow(conn, k)
  }

  it('stops the walk as soon as visit returns false', async () => {
    // The bound the op-log read depends on. Collecting everything and cutting
    // afterwards has already paid the memory the cap exists to refuse, so the
    // cut has to happen AT the cursor -- which means the rows past it must
    // never be materialized at all.
    const conn = connect('while-stop', 1)
    await seed(conn, ['a', 'b', 'c', 'd'])
    const db = await conn.open()

    const seen: string[] = []
    const tx = db.transaction(STORE, 'readonly')
    await forEachCursorWhile(tx.objectStore(STORE).openCursor(), (cursor) => {
      seen.push((cursor.value as { k: string }).k)
      return seen.length < 2
    })
    expect(seen).toEqual(['a', 'b'])
    conn.reset()
  })

  it('walks to exhaustion while visit keeps returning true', async () => {
    const conn = connect('while-all', 1)
    await seed(conn, ['a', 'b', 'c'])
    const db = await conn.open()

    const seen: string[] = []
    const tx = db.transaction(STORE, 'readonly')
    await forEachCursorWhile(tx.objectStore(STORE).openCursor(), (cursor) => {
      seen.push((cursor.value as { k: string }).k)
      return true
    })
    expect(seen).toEqual(['a', 'b', 'c'])
    conn.reset()
  })

  it('resolves without visiting anything on an empty range', async () => {
    const conn = connect('while-empty', 1)
    const db = await conn.open()
    const tx = db.transaction(STORE, 'readonly')
    let visits = 0
    await forEachCursorWhile(tx.objectStore(STORE).openCursor(), () => {
      visits++
      return true
    })
    expect(visits).toBe(0)
    conn.reset()
  })
})

// The TTL + entry-cap arithmetic both IDB stores sweep on. It was hand-written
// twice and was provably the same modulo the reserved term, so the two copies
// could only drift.
describe('selectSweepVictims', () => {
  const ttlMs = 1000
  const now = 10_000

  function at(...values: number[]): Array<{ at: number }> {
    return values.map(v => ({ at: v }))
  }

  it('selects nothing from an empty list', () => {
    expect(selectSweepVictims([], { now, ttlMs, maxEntries: 3 })).toEqual([])
  })

  it('selects every entry at or before the cutoff', () => {
    // 9000 is exactly `now - ttlMs`: the boundary is INCLUSIVE, matching the
    // `at <= cutoff` both stores documented.
    expect(selectSweepVictims(at(1000, 9000, 9001), { now, ttlMs })).toEqual(at(1000, 9000))
  })

  it('keeps everything when nothing has expired and the cap is not reached', () => {
    expect(selectSweepVictims(at(9500, 9600, 9700), { now, ttlMs, maxEntries: 5 })).toEqual([])
  })

  it('trims the oldest survivors down to the cap', () => {
    expect(selectSweepVictims(at(9500, 9600, 9700, 9800), { now, ttlMs, maxEntries: 2 }))
      .toEqual(at(9500, 9600))
  })

  it('counts reserved entries against the cap', () => {
    // Two reserved slots (the sweeping tab plus one live sibling) leave room
    // for one collectable survivor out of three.
    expect(selectSweepVictims(at(9500, 9600, 9700), { now, ttlMs, maxEntries: 3, reserved: 2 }))
      .toEqual(at(9500, 9600))
  })

  it('applies the TTL alone when maxEntries is omitted', () => {
    // The checkpoint sweep's foreign-account arm: expired rows go, but "this
    // account has too many tabs" says nothing about another account's rows.
    expect(selectSweepVictims(at(1000, 2000, 9500, 9600), { now, ttlMs }))
      .toEqual(at(1000, 2000))
  })

  it('combines both arms into one ascending prefix', () => {
    expect(selectSweepVictims(at(1000, 9500, 9600, 9700), { now, ttlMs, maxEntries: 2 }))
      .toEqual(at(1000, 9500))
  })

  it('never selects more than the whole list', () => {
    expect(selectSweepVictims(at(9500, 9600), { now, ttlMs, maxEntries: 0, reserved: 10 }))
      .toEqual(at(9500, 9600))
  })

  it('does not mutate its input', () => {
    const input = at(1000, 9500)
    selectSweepVictims(input, { now, ttlMs, maxEntries: 1 })
    expect(input).toEqual(at(1000, 9500))
  })
})

// An open connection MUST yield when another tab upgrades, or it blocks that
// tab's versionchange forever -- which the blocked side only ever sees as
// `onblocked`, so it degrades silently.
//
// These cases drive the yield with an explicit version bump because that is the
// cheapest way to provoke `versionchange` in a test. In PRODUCTION the trigger
// is the schema-repair `deleteDatabase()`, not a bump: `version` is a constant
// by design and both stores pass the same literal.
describe('createIdbConnection version-change handling', () => {
  it('does not block another opener\'s upgrade', async () => {
    const v1 = connect('yield', 1)
    const held = await v1.open()
    expect(held.objectStoreNames.contains(STORE)).toBe(true)

    // Without onversionchange this open is BLOCKED by the connection above --
    // the scaffold's onblocked arm rejects it, and the second tab silently
    // loses persistence for the rest of its life.
    const v2 = connect('yield', 2)
    await expect(v2.open()).resolves.toBeDefined()

    v1.reset()
    v2.reset()
  })

  it('drops the cached promise so the closed handle is never handed out again', async () => {
    // db.close() only sets the close-pending flag; the cached promise has to go
    // too, or every later open() returns this now-closed handle and its
    // transaction() throws InvalidStateError -- swallowed by every call site,
    // so both stores would go quietly dead for the page's lifetime.
    const v1 = connect('drop-cache', 1)
    const held = await v1.open()
    const v2 = connect('drop-cache', 2)
    await v2.open()

    // The handle we were holding is closed, so a transaction on it throws...
    expect(() => held.transaction(STORE, 'readonly')).toThrow()
    // ...and the connection no longer caches it: opening at the CURRENT version
    // yields a usable handle.
    const v2b = connect('drop-cache', 2)
    const fresh = await v2b.open()
    expect(fresh).not.toBe(held)
    await expect(
      requestToPromise(fresh.transaction(STORE, 'readonly').objectStore(STORE).count()),
    ).resolves.toBeTypeOf('number')

    v1.reset()
    v2.reset()
    v2b.reset()
  })
})

// An IDBOpenDBRequest cannot be cancelled. `onblocked` rejects the promise, but
// the request stays live: once the blocking connection closes, that SAME request
// still fires onupgradeneeded and onsuccess. The handle it produces is reachable
// from nothing — the promise it would have resolved is already rejected — so
// without an explicit close it stays open for the page's lifetime, and its
// `versionchange` handler goes on mutating the shared cached promise.
describe('createIdbConnection when an open is blocked', () => {
  it('closes the connection the blocked request eventually opens', async () => {
    // A raw handle with NO onversionchange, standing in for another tab that
    // does not yield. The scaffold's own connections always yield, so this
    // cannot be built with `connect`.
    const stubborn = await new Promise<IDBDatabase>((resolve) => {
      const r = indexedDB.open('orphan', 1)
      r.onupgradeneeded = () => {
        r.result.createObjectStore(STORE, { keyPath: 'k' })
      }
      r.onsuccess = () => resolve(r.result)
    })

    const v2 = connect('orphan', 2)
    await expect(v2.open()).rejects.toThrow(/blocked/)

    // Now the other tab goes away, which unblocks the request we already gave
    // up on. Count closes from this point: the only one that can occur is the
    // scaffold closing the handle nothing else can reach.
    // jsdom exposes no IDBDatabase global; reach the fake's prototype through
    // an instance so the spy covers the orphan too.
    const dbProto = Object.getPrototypeOf(stubborn) as { close: () => void }
    const closeSpy = vi.spyOn(dbProto, 'close')
    stubborn.close()
    closeSpy.mockClear()
    // The deferred upgrade + success run across several turns, and how many
    // depends on how loaded the runner is -- poll rather than assuming one.
    for (let i = 0; i < 50 && closeSpy.mock.calls.length === 0; i++)
      await new Promise(resolve => setTimeout(resolve, 0))

    expect(closeSpy).toHaveBeenCalled()
    closeSpy.mockRestore()
    v2.reset()
  })
})

// Schema repair, the alternative to bumping `version` on a store whose schema is
// still moving. It also covers what a version number CANNOT: a database left
// half-built by an upgrade that aborted partway carries the right version and
// the wrong stores, so no future bump would ever revisit it.
describe('createIdbConnection schema verification', () => {
  const INDEXED: IdbSchema = { [STORE]: { keyPath: 'k', indexes: { byAt: 'at' } } }

  /** Build a database at v1 with `schema`, then release it. */
  async function seedWith(name: string, schema: IdbSchema): Promise<void> {
    const conn = connect(name, 1, schema)
    ;(await conn.open()).close()
    conn.reset()
  }

  // The mechanism that replaces version bumps. Editing the declaration is the
  // whole change: an existing database that no longer matches is deleted and
  // rebuilt on next open, so no migration is written and no version moves.
  it('recreates a database missing an index the declaration adds', async () => {
    await seedWith('drifted', { [STORE]: { keyPath: 'k' } })

    const conn = connect('drifted', 1, INDEXED)
    const db = await conn.open()

    expect(db.version).toBe(1)
    expect(db.transaction(STORE, 'readonly').objectStore(STORE).indexNames.contains('byAt')).toBe(true)
    conn.reset()
  })

  it('recreates a database whose key path no longer matches', async () => {
    await seedWith('rekeyed', { [STORE]: { keyPath: 'k' } })

    const conn = connect('rekeyed', 1, { [STORE]: { keyPath: 'id' } })
    const db = await conn.open()

    expect(db.transaction(STORE, 'readonly').objectStore(STORE).keyPath).toBe('id')
    conn.reset()
  })

  it('recreates a database carrying a store the declaration dropped', async () => {
    // Left over from an earlier shape. Keeping it would let the database
    // accumulate every store the app ever had.
    await seedWith('orphaned', { [STORE]: { keyPath: 'k' }, leftover: { keyPath: 'k' } })

    const conn = connect('orphaned', 1, { [STORE]: { keyPath: 'k' } })
    const db = await conn.open()

    expect(Array.from(db.objectStoreNames)).toEqual([STORE])
    conn.reset()
  })

  it('leaves a conforming database alone, data and all', async () => {
    const seed = connect('conforming', 1, INDEXED)
    await putRow(seed, 'keep-me')
    ;(await seed.open()).close()
    seed.reset()

    const conn = connect('conforming', 1, INDEXED)
    expect(await countRows(conn)).toBe(1)
    conn.reset()
  })
})

describe('isIndexedDbAvailable', () => {
  it('is true when indexedDB is defined', () => {
    expect(isIndexedDbAvailable()).toBe(true)
  })

  it('is false without indexedDB (SSR / jsdom without the stub)', () => {
    vi.stubGlobal('indexedDB', undefined)
    expect(isIndexedDbAvailable()).toBe(false)
  })
})

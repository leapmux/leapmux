import type { IdbStores } from './idb'
import Dexie from 'dexie'
import { IDBFactory, IDBKeyRange } from 'fake-indexeddb'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  createIdbConnection,
  isIndexedDbAvailable,
  selectSweepVictims,
  stopWalk,
} from './idb'

beforeEach(() => {
  vi.stubGlobal('indexedDB', new IDBFactory())
  vi.stubGlobal('IDBKeyRange', IDBKeyRange)
})
afterEach(() => {
  vi.unstubAllGlobals()
})

const STORE = 'rows'

/** The declaration under test: primary key `k`, one index on `at`. */
const STORES: IdbStores = { [STORE]: 'k, at' }

/**
 * The native IndexedDB version every healthy database here carries.
 *
 * Dexie stores its declared version TIMES TEN, and `~/lib/idb` declares 1, so
 * anything other than 10 means something else wrote the database. Restated here
 * rather than imported so an assertion is an independent statement of the
 * number.
 */
const NATIVE_VERSION = 10

function connect(name: string, stores: IdbStores = STORES) {
  return createIdbConnection(name, stores)
}

async function putRow(conn: ReturnType<typeof connect>, k: string, at = 0): Promise<void> {
  const db = await conn.open()
  await db.table(STORE).put({ k, at })
}

async function countRows(conn: ReturnType<typeof connect>): Promise<number> {
  const db = await conn.open()
  return db.table(STORE).count()
}

/** The canonical on-disk shape, built by hand so a test can seed it at any version. */
function buildCanonical(db: IDBDatabase): void {
  db.createObjectStore(STORE, { keyPath: 'k' }).createIndex('at', 'at')
}

/**
 * Build `name` at NATIVE `version` with raw IndexedDB.
 *
 * Raw rather than through a second Dexie instance, because most of these shapes
 * are ones Dexie would never write -- an extra index, an orphaned store, a
 * legacy index name -- and the point of each is that the scaffold notices them
 * anyway.
 */
async function seedRaw(
  name: string,
  version: number,
  build: (db: IDBDatabase) => void,
  row?: Record<string, unknown>,
): Promise<void> {
  const db = await new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open(name, version)
    request.onupgradeneeded = () => build(request.result)
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error)
  })
  if (row) {
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction(STORE, 'readwrite')
      tx.objectStore(STORE).put(row)
      tx.oncomplete = () => resolve()
      tx.onerror = () => reject(tx.error)
    })
  }
  db.close()
}

/** Every store and index the opened database actually has. */
function shapeOf(db: Dexie): Record<string, string[]> {
  const idb = db.backendDB()
  const names = Array.from(idb.objectStoreNames)
  const tx = idb.transaction(names, 'readonly')
  return Object.fromEntries(names.map(name => [name, Array.from(tx.objectStore(name).indexNames)]))
}

describe('createIdbConnection', () => {
  it('creates the schema on a first open', async () => {
    const conn = connect('fresh')
    const db = await conn.open()
    expect(shapeOf(db)).toEqual({ [STORE]: ['at'] })
    // Dexie's ×10 encoding of the declared version 1. Every drift case below
    // reads this back, so pin it once here.
    expect(db.backendDB().version).toBe(NATIVE_VERSION)
    conn.reset()
  })

  it('caches the connection across calls', async () => {
    const conn = connect('cached')
    const [a, b] = await Promise.all([conn.open(), conn.open()])
    expect(a).toBe(b)
    conn.reset()
  })

  it('leaves a conforming database alone, data and all', async () => {
    // The regression guard for a check that always recreates: a database the
    // declaration already describes must keep its rows.
    const seed = connect('conforming')
    await putRow(seed, 'keep-me')
    seed.reset()

    const conn = connect('conforming')
    expect(await countRows(conn)).toBe(1)
    conn.reset()
  })

  it('drops the cached promise on a failed open so a later call retries', async () => {
    const openSpy = vi.spyOn(indexedDB, 'open')
    // A key path IndexedDB refuses, which fails the open inside Dexie's upgrade.
    const conn = connect('upgrade-throws', { [STORE]: '!!not a key path' })

    await expect(conn.open()).rejects.toBeDefined()
    // A retry must produce a fresh request rather than replay the cached
    // rejection forever; it fails again for the same reason, but it is a NEW
    // attempt, which is what lets a transient failure (quota, blocked) recover.
    await expect(conn.open()).rejects.toBeDefined()
    expect(openSpy.mock.calls.length).toBeGreaterThanOrEqual(2)
    openSpy.mockRestore()
    conn.reset()
  })

  it('leaves the database alone on a non-recreate failure', async () => {
    // Only the named open errors recreate. A transient failure -- quota, a
    // browser refusing the request -- must not be an excuse to destroy a cache
    // that is merely unreachable right now.
    const seeded = connect('non-recreate')
    await putRow(seeded, 'keep-me')
    seeded.reset()

    const openSpy = vi.spyOn(indexedDB, 'open').mockImplementation(() => {
      const request = {
        error: new DOMException('simulated quota failure', 'QuotaExceededError'),
      } as unknown as IDBOpenDBRequest & { onerror?: (event: unknown) => void }
      // A real Event, not a bare call: Dexie's error handler calls
      // `preventDefault()` on it and reads the error off `target`, so a handler
      // invoked with nothing throws inside Dexie and the open never settles.
      queueMicrotask(() => request.onerror?.({
        preventDefault: () => {},
        stopPropagation: () => {},
        target: request,
        type: 'error',
      }))
      return request
    })
    const failing = connect('non-recreate')
    await expect(failing.open()).rejects.toBeDefined()
    openSpy.mockRestore()
    failing.reset()

    const reader = connect('non-recreate')
    expect(await countRows(reader)).toBe(1)
    reader.reset()
  })
})

// The policy: any drift between the declaration and the stored database deletes
// and rebuilds it. Dexie implements NONE of this -- its own verifyInstalledSchema
// tests only for MISSING parts, and answers those by migrating -- so every case
// below is one the scaffold has to catch for itself.
describe('createIdbConnection schema drift', () => {
  /** Open through the scaffold and assert the database was rebuilt from scratch. */
  async function expectRebuilt(name: string): Promise<void> {
    const conn = connect(name)
    const db = await conn.open()
    expect(shapeOf(db)).toEqual({ [STORE]: ['at'] })
    expect(db.backendDB().version).toBe(NATIVE_VERSION)
    // The seeded row is gone: a rebuild, not a repair.
    expect(await db.table(STORE).count()).toBe(0)
    conn.reset()
  }

  it('recreates a database missing an index the declaration names', async () => {
    // THE HEADLINE CASE. This is the only drift Dexie's own verify detects, and
    // its answer is to reopen at native version + 1 and ADD the index, keeping
    // the rows -- a migration. The version assertion inside expectRebuilt is
    // what tells the two apart: 10 means rebuilt, 11 means Dexie patched.
    await seedRaw('missing-index', NATIVE_VERSION, (db) => {
      db.createObjectStore(STORE, { keyPath: 'k' })
    }, { k: 'stale', at: 1 })

    await expectRebuilt('missing-index')
  })

  it('recreates a database carrying an index the declaration dropped', async () => {
    // Dexie's verify never consults change.del, so this passes it silently.
    await seedRaw('extra-index', NATIVE_VERSION, (db) => {
      const store = db.createObjectStore(STORE, { keyPath: 'k' })
      store.createIndex('at', 'at')
      store.createIndex('extra', 'extra')
    }, { k: 'stale', at: 1 })

    await expectRebuilt('extra-index')
  })

  it('recreates a database carrying a store the declaration dropped', async () => {
    // Left over from an earlier shape, and invisible to Dexie's verify
    // (diff.del). Keeping it would let the database accumulate every store the
    // app ever had.
    await seedRaw('extra-store', NATIVE_VERSION, (db) => {
      buildCanonical(db)
      db.createObjectStore('leftover', { keyPath: 'k' })
    }, { k: 'stale', at: 1 })

    await expectRebuilt('extra-store')
  })

  it('recreates a database whose primary key path no longer matches', async () => {
    // getSchemaDiff files this under change.recreate with EMPTY add/change
    // arrays, and verifyInstalledSchema reads only those arrays -- so Dexie
    // opens it and runs against the wrong key path.
    await seedRaw('rekeyed', NATIVE_VERSION, (db) => {
      db.createObjectStore(STORE, { keyPath: 'id' }).createIndex('at', 'at')
    }, { id: 'stale', at: 1 })

    await expectRebuilt('rekeyed')
  })

  it('recreates a database whose index NAME differs from the declared key path', async () => {
    // Dexie names an index by its key path, so a pre-Dexie database calling this
    // index `byAt` has the right key path under the wrong name.
    //
    // THIS TEST GUARDS THE PRE-OPEN SNAPSHOT. Dexie's adjustToExistingIndexNames
    // runs on every open and renames the declaration's IndexSpec objects in
    // place to whatever the opened database calls the same key path, so a
    // fingerprint taken AFTER open() would match `byAt` and this case would
    // silently stop being detected.
    await seedRaw('renamed-index', NATIVE_VERSION, (db) => {
      db.createObjectStore(STORE, { keyPath: 'k' }).createIndex('byAt', 'at')
    }, { k: 'stale', at: 1 })

    await expectRebuilt('renamed-index')
  })

  it('recreates a pre-Dexie database whose shape does not match', async () => {
    // What a real `leapmux-crdt-state` looks like on the first load after the
    // move to Dexie: the hand-rolled scaffold wrote native version 1 and named
    // its indexes by hand, and Dexie names an index by its key path.
    await seedRaw('legacy-drifted', 1, (db) => {
      db.createObjectStore(STORE, { keyPath: 'k' }).createIndex('byAt', 'at')
    }, { k: 'stale', at: 1 })

    await expectRebuilt('legacy-drifted')
  })

  it('carries a pre-Dexie database forward when its shape already matches', async () => {
    // The deliberate exception, and the one case the version check CANNOT
    // reach: Dexie's 1 -> 10 upgrade runs during `open()`, before anything here
    // can look, so by the time the check reads the version it is already 10.
    //
    // Left that way on purpose rather than worked around. Catching it needs a
    // second raw open before every Dexie open, forever, on every cold start --
    // and what it would buy is deleting a cache whose shape IS the declaration
    // and whose rows the new code reads identically. That is a cost with no
    // benefit. What a real `leapmux-render-cache` looks like, so those survive.
    //
    // The version check still earns its keep on the two drifts that are
    // reachable once this build is live -- Dexie's auto-patch (native 11) and a
    // newer build (native 20) -- both covered above.
    await seedRaw('legacy-conforming', 1, buildCanonical, { k: 'keep-me', at: 1 })

    const conn = connect('legacy-conforming')
    const db = await conn.open()
    expect(shapeOf(db)).toEqual({ [STORE]: ['at'] })
    expect(db.backendDB().version).toBe(NATIVE_VERSION)
    expect(await db.table(STORE).count()).toBe(1)
    conn.reset()
  })

  it('recreates a database left NEWER by a rollback or a stale bundle', async () => {
    // A later build declaring version(2) writes native 20. Dexie does not fail
    // on that: it swallows the VersionError and RETRIES WITH NO VERSION, so it
    // attaches to the newer database happily and only the version check notices.
    await seedRaw('newer-build', 2 * NATIVE_VERSION, buildCanonical, { k: 'from-the-newer-build', at: 1 })

    await expectRebuilt('newer-build')
  })

  it('throws rather than spinning when the rebuild does not take', async () => {
    // A freshly built database that still fails the check means the two halves
    // of the scaffold disagree about the same declaration -- a bug here, which
    // no amount of deleting fixes. Exactly one retry, then give up.
    await seedRaw('undeletable', NATIVE_VERSION, (db) => {
      buildCanonical(db)
      db.createObjectStore('leftover', { keyPath: 'k' })
    })

    const deleteSpy = vi.spyOn(indexedDB, 'deleteDatabase').mockImplementation(() => {
      const request = {} as unknown as IDBOpenDBRequest & { onsuccess?: () => void }
      queueMicrotask(() => request.onsuccess?.())
      return request as unknown as IDBOpenDBRequest
    })
    const conn = connect('undeletable')
    await expect(conn.open()).rejects.toThrow(/still unusable after recreate/)
    deleteSpy.mockRestore()
    conn.reset()
  })
})

// An open connection MUST yield when another tab needs to delete the database,
// or it blocks that tab's repair -- which the blocked side only ever sees as
// `blocked`, so it degrades silently.
//
// The production trigger is the schema-repair delete, not a version bump: the
// declared version is a constant by design. These cases therefore drive it with
// a peer deleteDatabase, which is what actually happens.
describe('createIdbConnection version-change handling', () => {
  it('does not block a peer\'s delete', async () => {
    const held = connect('yield')
    await held.open()

    // Without the yield this delete stays blocked by the connection above.
    await expect(new Promise<void>((resolve, reject) => {
      const request = indexedDB.deleteDatabase('yield')
      request.onsuccess = () => resolve()
      request.onerror = () => reject(request.error)
      request.onblocked = () => reject(new Error('blocked by the cached connection'))
    })).resolves.toBeUndefined()

    held.reset()
  })

  it('drops the cached promise so the closed handle is never handed out again', async () => {
    // Dexie closes on `versionchange`, but the cached promise has to go too, or
    // every later open() returns this now-closed handle and its operations
    // reject DatabaseClosedError -- swallowed by every call site, so both stores
    // would go quietly dead for the page's lifetime.
    const conn = connect('drop-cache')
    const held = await conn.open()

    await new Promise<void>((resolve) => {
      const request = indexedDB.deleteDatabase('drop-cache')
      request.onsuccess = () => resolve()
      request.onerror = () => resolve()
      request.onblocked = () => resolve()
    })
    // Dexie's close is asynchronous relative to the delete; let it land.
    for (let i = 0; i < 50 && held.isOpen(); i++)
      await new Promise(resolve => setTimeout(resolve, 0))
    expect(held.isOpen()).toBe(false)

    // The connection no longer caches the dead handle: opening again yields a
    // usable one.
    const fresh = await conn.open()
    expect(fresh).not.toBe(held)
    await expect(fresh.table(STORE).count()).resolves.toBeTypeOf('number')
    conn.reset()
  })
})

// Dexie does not reject a blocked open -- it fires `blocked` and leaves the
// request pending forever. Without the race the scaffold adds, a caller behind
// another tab's older connection would wait indefinitely instead of degrading
// to a cold start.
describe('createIdbConnection when an open is blocked', () => {
  it('rejects instead of waiting for the other connection', async () => {
    // A raw handle with NO onversionchange, standing in for a tab that does not
    // yield. The scaffold's own connections always yield, so this cannot be
    // built with `connect`.
    const stubborn = await new Promise<IDBDatabase>((resolve) => {
      const request = indexedDB.open('orphan', 1)
      request.onupgradeneeded = () => buildCanonical(request.result)
      request.onsuccess = () => resolve(request.result)
    })

    const conn = connect('orphan')
    await expect(conn.open()).rejects.toThrow(/blocked/)

    stubborn.close()
    conn.reset()
  })

  // The rejection is only half of it. The raw request underneath keeps running,
  // so once the peer yields it OPENS -- and a connection nothing holds still
  // answers `versionchange`, which blocks the next schema repair's
  // `deleteDatabase` and leaves the store running against a drifted database.
  //
  // Closing inside the `blocked` handler looks like the fix and is the bug:
  // `close()` runs Dexie's `cancelOpen`, so `db.open()` REJECTS and the
  // fulfilled branch that would have closed the late handle never runs.
  it('closes the connection the blocked request eventually opens', async () => {
    const stubborn = await new Promise<IDBDatabase>((resolve) => {
      const request = indexedDB.open('orphan-late', 1)
      request.onupgradeneeded = () => buildCanonical(request.result)
      request.onsuccess = () => resolve(request.result)
    })
    // The prototype off a live instance: this environment supplies
    // fake-indexeddb's classes but installs no `IDBDatabase` global to spy on.
    const closes = vi.spyOn(Object.getPrototypeOf(stubborn) as { close: () => void }, 'close')

    const conn = connect('orphan-late')
    await expect(conn.open()).rejects.toThrow(/blocked/)
    const closesAtRejection = closes.mock.calls.length

    // The peer yields, so the scaffold's still-pending request completes. Its
    // handle must be closed: one call for `stubborn` itself, and one for the
    // connection nothing will ever hold.
    stubborn.close()
    await vi.waitFor(() => {
      expect(closes.mock.calls.length).toBeGreaterThan(closesAtRejection + 1)
    })

    conn.reset()
    closes.mockRestore()
  })
})

// The early-stop primitive both stores' bounded walks depend on. Collecting
// everything and cutting afterwards has already paid the memory the cap exists
// to refuse, so the cut has to happen AT the cursor.
describe('stopWalk', () => {
  it('ends a value walk at the row the visitor chooses', async () => {
    const conn = connect('stop-each')
    const db = await conn.open()
    await db.table(STORE).bulkPut([
      { k: 'a', at: 1 },
      { k: 'b', at: 2 },
      { k: 'c', at: 3 },
      { k: 'd', at: 4 },
    ])

    const seen: string[] = []
    // RESOLVES rather than rejects, which is the property the callers rely on:
    // Dexie's stop() resolves the iteration before it rebinds continue() to a
    // thrower, so the throw that follows lands on an already-settled promise.
    // A rejection here would discard the prefix every bounded read returns.
    await expect(db.table(STORE).orderBy('at').each((row, cursor) => {
      seen.push((row as { k: string }).k)
      if (seen.length === 2)
        stopWalk(cursor)
    })).resolves.toBeUndefined()

    expect(seen).toEqual(['a', 'b'])
    conn.reset()
  })

  it('ends a key-only walk, which never fetches a value at all', async () => {
    const conn = connect('stop-each-key')
    const db = await conn.open()
    await db.table(STORE).bulkPut([
      { k: 'a', at: 1 },
      { k: 'b', at: 2 },
      { k: 'c', at: 3 },
    ])

    const seen: number[] = []
    await db.table(STORE).orderBy('at').eachKey((at, cursor) => {
      seen.push(at as number)
      if (seen.length === 2)
        stopWalk(cursor)
    })

    expect(seen).toEqual([1, 2])
    conn.reset()
  })

  it('leaves a walk that never stops to run to exhaustion', async () => {
    const conn = connect('stop-never')
    const db = await conn.open()
    await db.table(STORE).bulkPut([{ k: 'a', at: 1 }, { k: 'b', at: 2 }])

    const seen: string[] = []
    await db.table(STORE).orderBy('at').each((row) => {
      seen.push((row as { k: string }).k)
    })

    expect(seen).toEqual(['a', 'b'])
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

// The suite must key exactly as a browser does, and one module-evaluation
// ordering decides whether it can.
describe('dexie key-range setup', () => {
  it('resolves maxKey to the real upper bound, not the string fallback', () => {
    // `Dexie.maxKey` is computed ONCE, at dexie's module evaluation, from
    // `Dexie.dependencies.IDBKeyRange`. `getMaxKey` self-replaces on the way:
    // with no global IDBKeyRange it throws inside its own try and permanently
    // rebinds itself to `'\uffff'`, which every later call gets -- including
    // the Dexie constructor's `this._maxKey` and DBCore's MAX_KEY. Passing
    // `IDBKeyRange` in the constructor options does not undo it, so compound
    // index prefixes would pad differently here than in a browser and the whole
    // suite would test something the app never does.
    //
    // `vitest.idbKeyRange.ts` installs the global ahead of every other setup
    // file for exactly this. This assertion is what fails if it stops running
    // first -- or if the install moves back into `vitest.setup.ts`, whose own
    // imports reach dexie before its body can run.
    expect(Dexie.maxKey).toEqual([[]])
  })
})

describe('isIndexedDbAvailable', () => {
  it('is true when indexedDB is defined', () => {
    expect(isIndexedDbAvailable()).toBe(true)
  })

  it('is false without indexedDB (SSR / jsdom without the stub)', () => {
    // A global IDBKeyRange stays installed by vitest.idbKeyRange.ts for Dexie's
    // module-scope maxKey, and deliberately does not make persistence
    // available: this answer reads `indexedDB` alone.
    vi.stubGlobal('indexedDB', undefined)
    expect(isIndexedDbAvailable()).toBe(false)
  })
})

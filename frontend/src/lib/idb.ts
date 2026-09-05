// ---------------------------------------------------------------------------
// Shared IndexedDB connection scaffold, over Dexie
//
// Every IDB-backed store in the app needs the same four things: an
// availability probe, a lazily-opened singleton connection whose promise is
// dropped on failure so a later call retries, a shape check, and a test hook to
// forget the cached connection after the IDBFactory is swapped. This module
// owns that skeleton so each store only declares its own schema.
//
// SCHEMA CHANGES RECREATE THE DATABASE. THERE ARE NO MIGRATIONS.
//
// This is the permanent policy, not a pre-release shortcut. Every database
// behind this scaffold is a CACHE over state the hub owns and re-syncs, so the
// worst a recreate can cost is one cold start -- and buying data-preserving
// migrations with that would mean writing, testing and forever maintaining an
// upgrade path per revision to protect data that is already safe elsewhere.
//
// DEXIE DOES NOT IMPLEMENT THAT POLICY, SO THE CHECK BELOW STILL DOES.
//
// Dexie's own `verifyInstalledSchema` tests only whether parts are MISSING
// (`diff.add`, `change.add`, `change.change`). Three drifts therefore pass it
// silently and leave Dexie running against a wrong-shaped database:
//
//   - a removed object store        (`diff.del`, never consulted)
//   - a removed index               (`change.del`, never consulted)
//   - a changed primary key         (`change.recreate`, whose add/change
//                                    arrays are empty)
//
// And the one drift it does see -- a missing store or index -- it answers by
// MIGRATING: it reopens at native version + 1 and adds the missing parts. That
// is exactly the upgrade path this module exists to not have.
//
// So a store declares its shape ONCE, as a Dexie `.stores()` spec, and this
// module derives both halves from it:
//
//   - the build, which Dexie performs, and
//   - the check, which asserts an opened database still matches and otherwise
//     deletes and rebuilds it.
//
// The check compares two fingerprints. The EXPECTED one is read from Dexie's
// own parse of the declaration, and the ACTUAL one from the raw IDBDatabase.
//
// A shape check catches what a version number CANNOT. A database left
// half-built by an aborted upgrade (a tab killed mid-versionchange, a quota
// failure part way) carries the RIGHT version and the wrong stores, so no
// future bump would ever revisit it. The declared version is therefore a
// constant that never has to move -- structurally so now, since
// `createIdbConnection` takes no version at all.
// ---------------------------------------------------------------------------

import type { DBCoreCursor } from 'dexie'
import Dexie from 'dexie'

/** Whether persistence can work here at all -- callers short-circuit synchronously on false. */
export function isIndexedDbAvailable(): boolean {
  return typeof indexedDB !== 'undefined'
}

/**
 * Pick the entries a TTL + entry-cap sweep should delete, from a
 * recency-ASCENDING list.
 *
 * Both IDB stores behind this scaffold sweep on the same two rules, and both
 * read their candidates off a `writtenAt`/`at` index key cursor, which yields
 * exactly that ascending order:
 *
 *   - TTL -- anything last touched at or before `now - ttlMs` goes.
 *   - CAP -- of the survivors, the oldest go until at most `maxEntries` remain,
 *     counting `reserved` entries that are exempt from collection but still
 *     occupy the budget (the sweeping tab's own row, its live siblings').
 *
 * Because the input is ascending, both arms select a PREFIX: every expired
 * entry precedes every fresh one, and the over-cap victims are the oldest of
 * what is left. So the result is `ascending.slice(0, ttlVictims + capVictims)`,
 * which is also why the two stores' hand-written arithmetic was provably the
 * same modulo the `reserved` term.
 *
 * Omit `maxEntries` to apply the TTL alone (the checkpoint sweep does this for
 * other accounts' rows: "this user has too many tabs" says nothing about how
 * many rows another account may keep).
 */
export function selectSweepVictims<T extends { at: number }>(
  ascending: readonly T[],
  opts: { now: number, ttlMs: number, maxEntries?: number, reserved?: number },
): T[] {
  const cutoff = opts.now - opts.ttlMs
  let expired = 0
  while (expired < ascending.length && ascending[expired]!.at <= cutoff)
    expired++
  const fresh = ascending.length - expired
  const overCap = opts.maxEntries === undefined
    ? 0
    : Math.max(0, fresh + (opts.reserved ?? 0) - opts.maxEntries)
  return ascending.slice(0, expired + overCap)
}

/**
 * Stop a Dexie cursor walk from inside `each` / `eachKey`.
 *
 * Dexie's public `Collection.until()` cannot serve either walk that needs this.
 * It filters on `cursor.value`, which a keys-only walk never fetches, and it
 * decides from the row alone, while the op-log walk decides from an
 * accumulator (the running frame and byte totals). The DBCoreCursor handed to
 * the callback exposes `stop()`; the public `each` typing narrows that argument
 * to `{key, primaryKey}`, hence the cast.
 *
 * `stop()` rebinds `cursor.continue` to a thrower, and Dexie's iterator calls
 * it right after the callback returns -- but that throw lands in Dexie's own
 * guarded callback and the walk terminates cleanly.
 */
export function stopWalk(cursor: { primaryKey: unknown }): void {
  (cursor as unknown as DBCoreCursor).stop()
}

/** A database's whole shape: object-store name -> that store's Dexie index spec. */
export type IdbStores = Record<string, string>

/** A lazily-opened, cached connection to one database. */
export interface IdbConnection<T extends Dexie = Dexie> {
  /** Open (or reuse) the connection. Rejects on open failure; the cache is cleared so a later call retries. */
  open: () => Promise<T>
  /** Visible for testing: forget the cached connection (e.g. after swapping the IDBFactory). */
  reset: () => void
}

/**
 * The one version any database behind this scaffold declares.
 *
 * It never moves: a schema change is a rebuild, not an upgrade. See the header.
 */
const DECLARED_VERSION = 1

/**
 * Dexie stores its declared version TIMES TEN (`db.verno = idbdb.version / 10`),
 * leaving the units digit for its own intermediate upgrades.
 */
const NATIVE_VERSION_FACTOR = 10

const EXPECTED_NATIVE_VERSION = DECLARED_VERSION * NATIVE_VERSION_FACTOR

/**
 * The open failures that mean "the stored database is unusable", and so are
 * answered by deleting and rebuilding it.
 *
 *   - VersionError  -- the stored database is NEWER than this build asks for.
 *     Dexie already retried versionless before surfacing this, so reaching here
 *     means even attaching to it failed.
 *   - UpgradeError  -- Dexie's own upgrader refused, which a changed primary
 *     key on a pre-Dexie database produces ("Not yet support for changing
 *     primary key").
 *   - SchemaError   -- Dexie rejected the declaration against what it found.
 *   - NotFoundError -- a store the declaration names was absent when Dexie
 *     built its middleware stacks.
 *
 * Deliberately NOT an unconditional retry. A QuotaExceededError, an
 * UnknownError or an AbortError still rejects WITHOUT deleting, so a transient
 * problem never destroys a cache that is merely unreachable right now.
 */
const RECREATE_ON_OPEN_ERROR: ReadonlySet<string> = new Set([
  'VersionError',
  'UpgradeError',
  'SchemaError',
  'NotFoundError',
])

/** Key paths compare as text: IndexedDB and Dexie both give a string or a string[]. */
function keyPathText(keyPath: string | readonly string[] | null | undefined): string {
  if (keyPath == null)
    return ''
  return Array.isArray(keyPath) ? `[${keyPath.join('+')}]` : String(keyPath)
}

/** One object store's shape, in the form both sides of the check can produce. */
interface StoreShape {
  name: string
  primKeyPath: string | readonly string[] | null | undefined
  autoIncrement: boolean
  indexes: Array<{
    name: string
    keyPath: string | readonly string[] | null | undefined
    unique: boolean
    multiEntry: boolean
  }>
}

/**
 * Reduce a whole database's shape to one comparable string.
 *
 * Total by construction: an extra or missing store, an extra or missing index,
 * a changed key path, a changed index NAME, and a changed autoIncrement /
 * unique / multiEntry flag all move it. Both lists are sorted, so declaration
 * order is not part of the identity.
 *
 * It deliberately omits the PRIMARY key's `unique` and `multi`. Dexie's parser
 * forces `primKey.unique = true` while IndexedDB exposes no such attribute for
 * a primary key at all, so comparing them could only ever produce a mismatch
 * that is not one.
 */
function fingerprint(stores: readonly StoreShape[]): string {
  return stores
    .map(store => [
      store.name,
      keyPathText(store.primKeyPath),
      store.autoIncrement ? '++' : '',
      store.indexes
        .map(index => `${index.name}=${keyPathText(index.keyPath)}${index.unique ? '&' : ''}${index.multiEntry ? '*' : ''}`)
        .sort()
        .join(','),
    ].join('|'))
    .sort()
    .join('\n')
}

/**
 * The shape the declaration asks for, read from Dexie's own parse of it.
 *
 * CALL THIS BETWEEN `.stores()` AND `.open()`, NEVER AFTER. `.stores()` fills
 * `db.tables` synchronously, but Dexie's `adjustToExistingIndexNames` runs on
 * EVERY open and RENAMES these very IndexSpec objects in place, to whatever the
 * database it just opened calls the same key path. A fingerprint taken
 * afterwards would therefore agree with any database whose key paths match --
 * including one still carrying an older build's index names, which is exactly
 * the drift the check exists to catch.
 */
function declaredFingerprint(db: Dexie): string {
  return fingerprint(db.tables.map(table => ({
    name: table.name,
    primKeyPath: table.schema.primKey.keyPath,
    autoIncrement: !!table.schema.primKey.auto,
    indexes: table.schema.indexes.map(index => ({
      name: index.name,
      keyPath: index.keyPath,
      unique: !!index.unique,
      multiEntry: !!index.multi,
    })),
  })))
}

/**
 * The shape the stored database actually has, read from the raw handle.
 *
 * NOT from `db.table(name).core.schema`: Dexie's virtual-index middleware
 * replaces that list with synthetic prefix entries of the compound primary key,
 * so it describes what Dexie can query, not what is on disk.
 *
 * `indexNames` and `keyPath` are only reachable through an object store, which
 * is only reachable through a transaction. Read-only and request-free, so it
 * commits on its own without touching a row.
 */
function installedFingerprint(idb: IDBDatabase): string {
  const names = Array.from(idb.objectStoreNames)
  if (names.length === 0)
    return ''
  const tx = idb.transaction(names, 'readonly')
  return fingerprint(names.map((storeName) => {
    const store = tx.objectStore(storeName)
    return {
      name: storeName,
      primKeyPath: store.keyPath,
      autoIncrement: store.autoIncrement,
      indexes: Array.from(store.indexNames).map((indexName) => {
        const index = store.index(indexName)
        return {
          name: indexName,
          keyPath: index.keyPath,
          unique: index.unique,
          multiEntry: index.multiEntry,
        }
      }),
    }
  }))
}

/** Carries the offending connection out of `openOnce`, so the repair path can close it. */
class SchemaDrift extends Error {
  constructor(readonly db: Dexie) {
    super(`indexedDB ${db.name}: stored schema does not match the declaration`)
    this.name = 'SchemaDrift'
  }
}

function errorName(err: unknown): string {
  return (err as { name?: string } | null | undefined)?.name ?? ''
}

/**
 * Delete a database outright, by name. Best-effort: every outcome resolves,
 * because the only callers are already on a degraded path and a failed delete
 * just means the reopen below fails too.
 *
 * `blocked` fires when another connection (another tab) still holds the
 * database. We do NOT wait for it: that tab is running the newer build and will
 * keep the handle open indefinitely, so blocking here would hang the open
 * forever instead of degrading to a cold start.
 *
 * This is why the repair path does not use Dexie's `db.delete()`, which is
 * otherwise the natural call: its promise settles on success or error only, and
 * a `blocked` delete leaves it PENDING FOREVER. Close the instance first, then
 * come here.
 */
function deleteDatabaseByName(name: string): Promise<void> {
  return new Promise<void>((resolve) => {
    const request = indexedDB.deleteDatabase(name)
    request.onsuccess = () => resolve()
    request.onerror = () => resolve()
    request.onblocked = () => resolve()
  })
}

/**
 * Create a cached connection to `name`, built from `stores`.
 *
 * `stores` is the single declaration of the database's shape: Dexie builds from
 * it AND it is checked against every handle handed out, so a database that does
 * not match is deleted and rebuilt. Change the declaration and existing
 * databases repair themselves on next open. See the module header for why that
 * is the permanent policy here.
 *
 * A rejected open drops the cached promise, so the next call retries rather
 * than latching the failure for the page's lifetime.
 */
export function createIdbConnection<T extends Dexie = Dexie>(
  name: string,
  stores: IdbStores,
): IdbConnection<T> {
  let dbPromise: Promise<Dexie> | null = null

  /**
   * Whether `db` is the database the declaration asks for.
   *
   * Two independent tests, and neither alone is sufficient.
   */
  function shapeMatches(db: Dexie, expected: string): boolean {
    const idb = db.backendDB()
    // The VERSION. There is no legitimate path to a native version other than
    // DECLARED_VERSION * 10 for a database this module built, so any other
    // value proves something else wrote it:
    //   - Dexie's own auto-patch, which reopens at version + 1 to ADD the parts
    //     its verify found missing. That is a migration; see the header.
    //   - a NEWER build, whose `version(2)` is native 20. Dexie does not fail
    //     on that: it swallows the VersionError and RETRIES VERSIONLESS, so it
    //     attaches happily and this is the only place that notices.
    //
    // ONE CASE THIS CANNOT REACH, deliberately. The hand-rolled scaffold this
    // replaced wrote native version 1, and Dexie upgrades such a database to 10
    // DURING `open()` -- before anything here can look -- so a legacy database
    // whose shape already matches the declaration is carried forward with its
    // rows. Catching it would need a second raw open before every Dexie open,
    // on every cold start, forever; what that buys is deleting a cache whose
    // shape IS the declaration and whose rows this build reads identically. A
    // legacy database whose shape has since MOVED is still rebuilt, by the
    // fingerprint below.
    if (idb.version !== EXPECTED_NATIVE_VERSION)
      return false
    // And the SHAPE, because the version alone cannot see the three drifts
    // Dexie's own verify passes silently -- a removed store, a removed index,
    // and a changed primary key all leave the version at 10.
    try {
      return installedFingerprint(idb) === expected
    }
    catch {
      return false
    }
  }

  /** One open attempt, ending in a connection that has passed the shape check. */
  async function openOnce(): Promise<Dexie> {
    // CONSTRUCTED PER ATTEMPT, never once at module scope. Dexie snapshots
    // `indexedDB` and `IDBKeyRange` into `db._deps` in its CONSTRUCTOR, from the
    // options given here -- and its own `Dexie.dependencies` defaults were read
    // off the globals at MODULE EVALUATION. Passing them explicitly, from a
    // constructor that runs per attempt, is what lets a test swap the
    // IDBFactory after import and still get a connection into the new universe.
    const db = new Dexie(name, {
      indexedDB,
      IDBKeyRange,
      // Load-bearing: nothing may run against a database that has not passed
      // shapeMatches. It also disables Dexie's `idbdb.onclose` auto-reopen,
      // which would otherwise reopen WITHOUT the check.
      autoOpen: false,
      // There is no liveQuery here, so Dexie's query cache buys nothing and
      // costs a second consistency surface plus a deep clone per read.
      cache: 'disabled',
    })
    db.version(DECLARED_VERSION).stores(stores)
    // BEFORE open(). See declaredFingerprint for why the order is not a style
    // choice.
    const expected = declaredFingerprint(db)

    let settled = false
    const opened = await new Promise<Dexie>((resolve, reject) => {
      // Dexie does NOT reject a blocked open. It fires this event and leaves
      // the request pending while another tab holds an older connection, so
      // without this the caller waits forever instead of degrading. Closing
      // with disableAutoOpen cancels Dexie's own pending open, which is what
      // replaces the orphaned-handle bookkeeping a raw IDBOpenDBRequest needs.
      db.on('blocked', () => {
        if (settled)
          return
        settled = true
        db.close({ disableAutoOpen: true })
        reject(new Error(`indexedDB ${name} open blocked`))
      })
      db.open().then(
        () => {
          // A `blocked` event already rejected for us and the open completed
          // anyway. Close the connection nothing will ever hold.
          if (settled) {
            db.close({ disableAutoOpen: true })
            return
          }
          settled = true
          resolve(db)
        },
        (err: unknown) => {
          if (settled)
            return
          settled = true
          reject(err)
        },
      )
    })

    if (!shapeMatches(opened, expected))
      throw new SchemaDrift(opened)
    return opened
  }

  /**
   * Delete and rebuild, exactly ONCE.
   *
   * A freshly built database that still fails the check means Dexie and
   * `installedFingerprint` disagree about the same declaration, which is a bug
   * in this module -- and no amount of deleting fixes it, so spinning would
   * just hang every caller.
   */
  async function recreate(): Promise<Dexie> {
    await deleteDatabaseByName(name)
    try {
      return await openOnce()
    }
    catch (err) {
      if (err instanceof SchemaDrift) {
        err.db.close({ disableAutoOpen: true })
        throw new Error(`indexedDB ${name}: schema still unusable after recreate`)
      }
      throw err
    }
  }

  function open(): Promise<T> {
    if (!dbPromise) {
      // Every path that drops the cache compares against THIS attempt first.
      // Nulling the shared variable unconditionally let a stale attempt clobber
      // a newer, healthy connection: a rejection (or a `close` on a superseded
      // handle) settling after `reset()` had already installed a different
      // promise would drop that one instead, and the next caller reopened for
      // no reason while the discarded handle stayed open.
      let attempt: Promise<Dexie>
      const invalidate = (): void => {
        if (dbPromise === attempt)
          dbPromise = null
      }
      attempt = openOnce()
        .catch(async (err: unknown) => {
          if (err instanceof SchemaDrift) {
            err.db.close({ disableAutoOpen: true })
            return await recreate()
          }
          if (!RECREATE_ON_OPEN_ERROR.has(errorName(err)))
            throw err
          return await recreate()
        })
        .then((db) => {
          // Registered only now, AFTER the repair path above, so the deliberate
          // closes in it cannot invalidate a cache entry they do not own.
          //
          // `close` fires when a peer tab's schema repair deletes this database
          // (versionchange -> Dexie's default handler closes) and when the
          // browser closes the connection abnormally. Dropping the cached
          // promise is REQUIRED, not tidiness: every later open() would
          // otherwise hand back this closed handle, whose operations reject
          // DatabaseClosedError -- and every call site here swallows, so both
          // stores would go quietly dead for the page's lifetime.
          db.on('close', invalidate)
          // Closes the window between the shape check and the line above: a
          // close that landed in it fired no handler and would leave a dead
          // handle cached.
          if (!db.isOpen())
            invalidate()
          return db
        })
      void attempt.catch(invalidate)
      dbPromise = attempt
    }
    return dbPromise as Promise<T>
  }

  function reset(): void {
    // Nulled FIRST, so the `close` handler's identity check sees a mismatch and
    // does not clear a promise a concurrent open() may already have installed.
    const current = dbPromise
    dbPromise = null
    void current?.then(db => db.close({ disableAutoOpen: true })).catch(() => {})
  }

  return { open, reset }
}

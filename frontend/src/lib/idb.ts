// ---------------------------------------------------------------------------
// Shared IndexedDB connection scaffold
//
// Every IDB-backed store in the app needs the same four things: an
// availability probe, a lazily-opened singleton connection whose promise is
// dropped on failure so a later call retries, a request->promise adapter, and
// a test hook to forget the cached connection after the IDBFactory is swapped.
// This module owns that skeleton so each store only declares its own schema.
//
// SCHEMA CHANGES RECREATE THE DATABASE. THERE ARE NO MIGRATIONS.
//
// This is the permanent policy, not a pre-release shortcut. Every database
// behind this scaffold is a CACHE over state the hub owns and re-syncs, so the
// worst a recreate can cost is one cold start -- and buying data-preserving
// migrations with that would mean writing, testing and forever maintaining an
// upgrade path per revision to protect data that is already safe elsewhere.
//
// So a store declares its shape ONCE, as an `IdbSchema`, and this module
// derives both halves from it:
//
//   - the upgrade, which builds exactly those stores and indexes, and
//   - the check, which asserts an opened database still matches and otherwise
//     deletes and rebuilds it.
//
// Deriving both from one declaration is the point. When they were two
// hand-written functions, adding an index meant remembering to update the
// checker too, and forgetting made the repair silently stop repairing -- the
// database would open, be wrong, and fail at the first cursor instead.
//
// This also catches what a version number CANNOT. A database left half-built
// by an aborted upgrade (a tab killed mid-versionchange, a quota failure part
// way) carries the RIGHT version and the wrong stores, so no future bump would
// ever revisit it; a shape check sees it immediately. `version` is therefore a
// constant that never has to move, and the VersionError arm below still handles
// the reverse case -- a database left NEWER by a rollback or a stale bundle.
// ---------------------------------------------------------------------------

/** Whether persistence can work here at all -- callers short-circuit synchronously on false. */
export function isIndexedDbAvailable(): boolean {
  return typeof indexedDB !== 'undefined'
}

/** Adapt a one-shot IDBRequest to a promise. */
export function requestToPromise<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error ?? new Error('indexedDB request failed'))
  })
}

/**
 * Walk a cursor request, calling `visit` for each position until it returns
 * false or the cursor is exhausted.
 *
 * The stopping form exists because an unbounded walk is a hazard in its own
 * right: a store whose rows are appended faster than they are compacted grows
 * without bound, and a walk that only decides what to keep AFTER materializing
 * everything has already paid the memory it was meant to cap. Callers that
 * genuinely want the whole range use `forEachCursor` below.
 *
 * `C` infers per call site: `openCursor()` is `IDBRequest<IDBCursorWithValue |
 * null>` (so `visit` sees `.value`), `openKeyCursor()` is
 * `IDBRequest<IDBCursor | null>` (primary keys only).
 *
 * Resolves when the walk ends (exhausted or stopped). Rejects on a cursor
 * error, which inside a readwrite transaction leaves that transaction to abort
 * as usual.
 */
export function forEachCursorWhile<C extends IDBCursor>(
  request: IDBRequest<C | null>,
  visit: (cursor: C) => boolean,
): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    request.onsuccess = () => {
      const cursor = request.result
      if (!cursor || !visit(cursor)) {
        resolve()
        return
      }
      cursor.continue()
    }
    request.onerror = () => reject(request.error ?? new Error('indexedDB cursor failed'))
  })
}

/**
 * Walk a cursor request to exhaustion, calling `visit` for each position.
 *
 * The `onsuccess` / `cursor.continue()` / `onerror`-reject dance is identical
 * for every cursor walk and differs only in the loop body, so it lives here
 * next to `requestToPromise` rather than being hand-rolled per store. Callers
 * keep their own accumulator (push into a local array) or side effect (delete
 * by primary key); the key range stays in the request the caller builds, so
 * this needs no options.
 *
 * `visit`'s return value is ignored — deliberately typed `void` so a concise
 * arrow body (`cursor => rows.push(cursor.value)`, which returns a number) is
 * accepted. Use `forEachCursorWhile` when the walk needs to stop early.
 */
export function forEachCursor<C extends IDBCursor>(
  request: IDBRequest<C | null>,
  visit: (cursor: C) => void,
): Promise<void> {
  return forEachCursorWhile(request, (cursor) => {
    visit(cursor)
    return true
  })
}

/**
 * Pick the entries a TTL + entry-cap sweep should delete, from a
 * recency-ASCENDING list.
 *
 * Both IDB stores behind this scaffold sweep on the same two rules, and both
 * read their candidates off a `writtenAt`/`at` index key cursor, which yields
 * exactly that ascending order:
 *
 *   - TTL — anything last touched at or before `now - ttlMs` goes.
 *   - CAP — of the survivors, the oldest go until at most `maxEntries` remain,
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
 * Adapt a transaction's terminal events to a promise: resolves on `complete`,
 * rejects on `error` or `abort`.
 *
 * Every readwrite transaction needs the identical three-handler dance and they
 * differ only in the label that names the failure, so it lives here beside
 * `requestToPromise` and `forEachCursor` rather than being hand-rolled per
 * store.
 *
 * Register this in the SAME task that created the transaction — an `await`
 * before it lets the transaction auto-deactivate at the microtask checkpoint
 * and the `complete` event can be missed entirely.
 */
export function txToPromise(tx: IDBTransaction, label: string): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    tx.oncomplete = () => resolve()
    tx.onerror = () => reject(tx.error ?? new Error(`indexedDB ${label} failed`))
    tx.onabort = () => reject(tx.error ?? new Error(`indexedDB ${label} aborted`))
  })
}

/** One object store's shape. */
export interface IdbStoreSchema {
  /** Primary key path. Omit for an out-of-line key. */
  keyPath?: string | string[]
  /** Whether the store assigns the primary key. */
  autoIncrement?: boolean
  /** Index name -> its key path. */
  indexes?: Record<string, string | string[]>
}

/** A database's whole shape: object-store name -> that store's schema. */
export type IdbSchema = Record<string, IdbStoreSchema>

/**
 * Build every store and index in `schema`, dropping whatever is already there.
 *
 * Runs inside the versionchange transaction. Recreating rather than reconciling
 * is the module's policy (see the header): these databases are caches, so a
 * rebuild costs a cold start and nothing else, and it makes the resulting shape
 * a function of the declaration alone rather than of the path taken to reach it.
 *
 * Stores NOT in the schema are dropped too -- they are leftovers from an earlier
 * shape, and keeping them would let the database accumulate every store the app
 * ever had.
 */
function applySchema(db: IDBDatabase, schema: IdbSchema): void {
  for (const existing of Array.from(db.objectStoreNames))
    db.deleteObjectStore(existing)
  for (const [name, spec] of Object.entries(schema)) {
    const store = db.createObjectStore(name, {
      keyPath: spec.keyPath,
      autoIncrement: spec.autoIncrement ?? false,
    })
    for (const [indexName, indexKeyPath] of Object.entries(spec.indexes ?? {}))
      store.createIndex(indexName, indexKeyPath)
  }
}

/** Key paths compare structurally: IndexedDB returns a string or a string[]. */
function samePath(a: string | string[] | null, b: string | string[] | undefined): boolean {
  if (Array.isArray(a) || Array.isArray(b))
    return Array.isArray(a) && Array.isArray(b) && a.length === b.length && a.every((v, i) => v === b[i])
  return (a ?? undefined) === b
}

/**
 * Whether `db` matches `schema` exactly -- same stores, same key paths, same
 * indexes. A false answer deletes and rebuilds the database.
 *
 * Derived from the SAME declaration `applySchema` builds from, so the two
 * cannot disagree about what the schema is.
 */
function schemaMatches(db: IDBDatabase, schema: IdbSchema): boolean {
  const expected = Object.keys(schema)
  if (db.objectStoreNames.length !== expected.length)
    return false
  if (!expected.every(name => db.objectStoreNames.contains(name)))
    return false
  // indexNames and keyPath are only reachable through an object store, which is
  // only reachable through a transaction. Read-only and request-free, so it
  // commits on its own without touching a row.
  const tx = db.transaction(expected, 'readonly')
  return expected.every((name) => {
    const spec = schema[name]!
    const store = tx.objectStore(name)
    if (!samePath(store.keyPath, spec.keyPath) || store.autoIncrement !== (spec.autoIncrement ?? false))
      return false
    const indexes = Object.entries(spec.indexes ?? {})
    if (store.indexNames.length !== indexes.length)
      return false
    return indexes.every(([indexName, indexKeyPath]) =>
      store.indexNames.contains(indexName) && samePath(store.index(indexName).keyPath, indexKeyPath),
    )
  })
}

/** A lazily-opened, cached connection to one database. */
export interface IdbConnection {
  /** Open (or reuse) the connection. Rejects on open failure; the cache is cleared so a later call retries. */
  open: () => Promise<IDBDatabase>
  /** Visible for testing: forget the cached connection (e.g. after swapping the IDBFactory). */
  reset: () => void
}

/**
 * Create a cached connection to `name` at `version`, built from `schema`.
 *
 * `schema` is the single declaration of the database's shape: it builds the
 * stores on creation AND is checked against every handle handed out, so a
 * database that does not match is deleted and rebuilt. Change the schema and
 * existing databases repair themselves on next open -- `version` does not have
 * to move. See the module header for why that is the permanent policy here.
 *
 * A rejected open drops the cached promise, so the next call retries rather
 * than latching the failure for the page's lifetime.
 */
export function createIdbConnection(
  name: string,
  version: number,
  schema: IdbSchema,
): IdbConnection {
  let dbPromise: Promise<IDBDatabase> | null = null

  /**
   * Delete the database outright. Best-effort: every outcome resolves, because
   * the only caller is already on a degraded path and a failed delete just
   * means the retry below fails too.
   *
   * `blocked` fires when another connection (another tab) still holds the
   * database. We do NOT wait for it: that tab is running the newer build and
   * will keep the handle open indefinitely, so blocking here would hang the
   * open forever instead of degrading to a full snapshot.
   */
  function deleteDatabase(): Promise<void> {
    return new Promise<void>((resolve) => {
      const request = indexedDB.deleteDatabase(name)
      request.onsuccess = () => resolve()
      request.onerror = () => resolve()
      request.onblocked = () => resolve()
    })
  }

  /**
   * One open attempt. `invalidate` drops the cached promise IF it still refers
   * to the attempt that owns this handle -- see `open()` for why the identity
   * check is load-bearing.
   */
  function openOnce(invalidate: () => void): Promise<IDBDatabase> {
    return new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open(name, version)
      // An IDBOpenDBRequest cannot be cancelled. `onblocked` below rejects, but
      // the request stays LIVE: once the other tab yields its connection, the
      // very same request goes on to fire onupgradeneeded and then onsuccess.
      // Without this flag that late success resolves an already-settled promise
      // (a no-op) while leaving a real IDBDatabase that nothing captures and
      // nothing ever closes -- one leaked connection per blocked open, each of
      // which still answers `versionchange` and would null the SHARED cached
      // promise. Closing it here is the only handle we will ever have on it.
      let settled = false
      request.onupgradeneeded = () => {
        // Builds the declared shape from scratch, whether this is a first
        // creation or a rebuild after a mismatch. No migration path exists to
        // get wrong -- see the module header.
        applySchema(request.result, schema)
      }
      request.onsuccess = () => {
        const db = request.result
        if (settled) {
          db.close()
          return
        }
        settled = true
        // Yield when ANOTHER tab starts an upgrade. Without this, our open
        // connection blocks that tab's versionchange forever -- it only sees
        // `onblocked` below and gives up, which silently disables persistence
        // for it.
        //
        // In production that upgrade is NOT a version bump: `version` is a
        // constant here by design (see the module header), and both stores pass
        // the same literal. The trigger is the `deleteDatabase()` in the
        // schema-repair path below -- a peer tab that opened this database,
        // found a stale schema, and is dropping it to rebuild. So this handler
        // is on the normal repair route, not a someday-migration route.
        //
        // Dropping the cached promise is REQUIRED, not tidiness: db.close()
        // only sets the close-pending flag (in-flight transactions still run
        // to completion, so nothing is aborted), but every later open() would
        // otherwise hand back this now-closed handle, whose transaction()
        // throws InvalidStateError -- and every caller here swallows, so both
        // stores would go quietly dead for the page's lifetime.
        db.onversionchange = () => {
          db.close()
          invalidate()
        }
        resolve(db)
      }
      request.onerror = () => {
        if (settled)
          return
        settled = true
        reject(request.error ?? new Error('indexedDB open failed'))
      }
      // Blocked by another tab holding an older connection: degrade now; the
      // cached promise is dropped below so a later call retries. With
      // onversionchange in place above, that retry now succeeds once the
      // other tab yields, instead of re-blocking forever.
      request.onblocked = () => {
        if (settled)
          return
        settled = true
        reject(new Error('indexedDB open blocked'))
      }
    })
  }

  function open(): Promise<IDBDatabase> {
    if (!dbPromise) {
      // Every path that drops the cache compares against THIS attempt first.
      // Nulling the shared variable unconditionally let a stale attempt clobber
      // a newer, healthy connection: a rejection (or a `versionchange` on a
      // superseded handle) settling after `reset()` had already installed a
      // different promise would drop that one instead, and the next caller
      // reopened for no reason while the discarded handle stayed open.
      let attempt: Promise<IDBDatabase>
      const invalidate = (): void => {
        if (dbPromise === attempt)
          dbPromise = null
      }
      /**
       * Hand back a database that matches the declaration, rebuilding it if the
       * one we opened does not. This is how a schema change lands without a
       * version bump; see the module header for why that is the policy.
       *
       * Exactly ONE retry. A freshly built database that still fails the check
       * means applySchema and schemaMatches disagree about the same
       * declaration, which is a bug in this module -- and no amount of deleting
       * fixes it, so spinning would just hang every caller.
       */
      const ensureSchema = async (db: IDBDatabase): Promise<IDBDatabase> => {
        if (schemaMatches(db, schema))
          return db
        db.close()
        await deleteDatabase()
        const fresh = await openOnce(invalidate)
        if (schemaMatches(fresh, schema))
          return fresh
        fresh.close()
        throw new Error(`indexedDB ${name}: schema still unusable after recreate`)
      }
      attempt = openOnce(invalidate).catch(async (err: unknown) => {
        // The stored database is NEWER than the version this build asks for.
        //
        // IndexedDB does not downgrade: opening at a lower version fails the
        // request outright with a VersionError, and because every call site
        // here swallows, persistence would be silently and PERMANENTLY off for
        // that profile. It is a routine situation, not an exotic one -- a
        // rollback to a previous deploy, a stale cached bundle, or an older
        // desktop build launched after a newer one all produce it.
        //
        // Both databases behind this scaffold are pure caches over data that
        // can be rebuilt (render artifacts re-render; the CRDT checkpoint
        // re-fetches as one full snapshot), so discarding the newer schema
        // costs a cold start and nothing else -- strictly better than running
        // with persistence dead. It is the same reasoning as the schema-mismatch
        // rebuild above: no database here holds anything the hub cannot re-supply.
        //
        // NOTE this is deliberately NOT an unconditional retry: only a
        // VersionError is recreated from. A quota failure or a genuine open
        // error still rejects, so a transient problem does not destroy the
        // cache.
        if ((err as DOMException | undefined)?.name !== 'VersionError')
          throw err
        await deleteDatabase()
        return openOnce(invalidate)
      }).then(ensureSchema)
      void attempt.catch(invalidate)
      dbPromise = attempt
    }
    return dbPromise
  }

  function reset(): void {
    void dbPromise?.then(db => db.close()).catch(() => {})
    dbPromise = null
  }

  return { open, reset }
}

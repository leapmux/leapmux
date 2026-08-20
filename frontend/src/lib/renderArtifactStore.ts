import type { IdbSchema } from './idb'
import { createIdbConnection, forEachCursor, isIndexedDbAvailable, requestToPromise, selectSweepVictims } from './idb'
import { fnv1a32Hex } from './stringDigest'

// ---------------------------------------------------------------------------
// Persistent (IndexedDB) store for rendered artifacts
//
// The in-memory render caches (markdown HTML, Shiki tokens) die with the page,
// so every reload was a fully cold render: worker spawn, Oniguruma WASM
// compile, grammar loads, and a re-highlight of everything in the restored
// window. This store persists those artifacts across reloads, keyed by content
// digest and VERIFIED against the exact source on read, so a warm reload can
// serve final HTML/tokens without touching a worker.
//
// Staleness across DEPLOYMENTS is the real hazard: a persisted artifact
// outlives the bundle that produced it, so anything build-dependent in the
// artifact (pipeline output shape, sanitizer schema, Shiki theme contract —
// and any build-hashed class name someone later lets leak into rendered HTML)
// would silently rot. Consumers guard this by folding a pipeline fingerprint
// into their namespace (see RENDER_ARTIFACT_CACHE_VERSION below): a fingerprint
// change orphans every old entry wholesale, and the TTL sweep deletes them.
//
// Every operation is best-effort and no-throw: without indexedDB (jsdom, SSR)
// or when it fails (private browsing, quota), reads miss and writes drop.
// ---------------------------------------------------------------------------

/**
 * Schema/fingerprint version folded into every consumer namespace. Persisted
 * entries survive deployments, so the invariant is: BUMP THIS whenever the
 * NEW bundle could misinterpret or mis-render an artifact written by an OLD
 * bundle. For any change, ask "would HTML/tokens persisted last week still be
 * read and rendered correctly by this code?" — if not, bump.
 *
 * Bump for:
 * - Persisted value SHAPE changes — a consumer's stored value layout (e.g.
 *   the markdown artifact's {h, s} record, the interned token wire shape),
 *   where old values fail or, worse, PASS the new read validation wrongly.
 * - Rendered-markup CONTRACT changes — markdown pipeline/plugin/sanitizer
 *   output, the shared style-class naming scheme or canonical declaration
 *   format (shikiStyleClass), or any markup/attributes the consuming CSS or
 *   renderers key off — anything where old markup would render wrongly under
 *   the new consumers. A build-hashed (vanilla-extract) class leaking into
 *   rendered HTML is the canonical example: it changes EVERY build, so it
 *   must never appear in an artifact at all.
 *
 * No bump needed for:
 * - Changes that touch neither the persisted bytes nor how they are
 *   interpreted: refactors, in-memory cache policy, CSS that targets
 *   structural selectors (pre.shiki span, [data-shiki-token]).
 * - Shiki theme changes — the consumer namespaces fold the theme names in
 *   separately (see `markdownArtifactNs()` / `tokenArtifactNs()`). Those are
 *   FUNCTIONS, not constants: the syntax theme is a run-time preference, so the
 *   namespace a caller writes under is the one live at its dispatch, and entries
 *   written under an abandoned theme are simply never looked up again.
 * - Output that merely gets BETTER but stays valid (e.g. tighter token
 *   merging): old artifacts still render correctly, just less optimally.
 *   Bumping anyway is a judgment call to re-render the population uniformly.
 */
export const RENDER_ARTIFACT_CACHE_VERSION = 2

/** Entries older than this are dropped by the sweep (TTL since last use). */
export const ARTIFACT_TTL_MS = 7 * 24 * 60 * 60 * 1000

/**
 * A read only rewrites the record's recency stamp when the stored `at` is at
 * least this stale. The stamp feeds only the 7-day TTL and the oldest-first cap
 * -- both indifferent to sub-hour precision -- so refreshing an already-recent
 * entry buys nothing but re-serializes and re-writes the WHOLE payload (up to
 * ~512 KB of HTML / a large token array) to IndexedDB on every hit. On a warm
 * reload that is one redundant full-payload write per restored row; gating it on
 * a coarse interval (1h << the TTL) keeps "hot entries outlive the sweep" while
 * eliminating the per-read write in the common case.
 */
export const ARTIFACT_TOUCH_INTERVAL_MS = 60 * 60 * 1000

/** Global entry cap across all namespaces, enforced oldest-first by the sweep. */
export const ARTIFACT_MAX_ENTRIES = 2000

const DB_NAME = 'leapmux-render-cache'
// A CONSTANT. Schema changes land by editing SCHEMA below, not by bumping this:
// the scaffold rebuilds any database that does not match the declaration. See
// ~/lib/idb's header for why recreating rather than migrating is the policy --
// this store is a cache over artifacts the app can always re-render.
const DB_VERSION = 1
const STORE_NAME = 'artifacts'
const AT_INDEX = 'at'

interface ArtifactRecord {
  /** `${ns}:${digest}:${length}` — see artifactKey. */
  k: string
  /** The exact source input, for collision verification on read. */
  source: string
  value: unknown
  /** Last-used timestamp (refreshed on read), the sweep's recency key. */
  at: number
}

/** Whether persistence can work here at all — callers short-circuit synchronously on false. */
export function isArtifactStoreAvailable(): boolean {
  return isIndexedDbAvailable()
}

/**
 * The record key. The 32-bit digest alone could collide across distinct
 * sources; the length term makes that rarer and the stored `source` check on
 * read makes it harmless (a mismatch is a miss, never a wrong artifact).
 */
function artifactKey(ns: string, source: string): string {
  return `${ns}:${fnv1a32Hex(source)}:${source.length.toString(36)}`
}

/**
 * The database's shape, in one place. Builds the store on creation and is
 * checked against every opened database, which is rebuilt if it does not match
 * -- see ~/lib/idb's header. `AT_INDEX` carries the sweep's recency ordering,
 * so a database missing it would throw on first use rather than degrade.
 */
const SCHEMA: IdbSchema = {
  [STORE_NAME]: {
    keyPath: 'k',
    indexes: { [AT_INDEX]: 'at' },
  },
}

const connection = createIdbConnection(DB_NAME, DB_VERSION, SCHEMA)

const openDb = connection.open

/** Visible for testing: forget the cached connection (e.g. after swapping the IDBFactory). */
export function _resetArtifactStoreForTest(): void {
  connection.reset()
}

function putRecord(db: IDBDatabase, record: ArtifactRecord): Promise<void> {
  return requestToPromise(
    db.transaction(STORE_NAME, 'readwrite').objectStore(STORE_NAME).put(record),
  ).then(() => {})
}

/**
 * Read an artifact. Resolves undefined on any miss, mismatch, or failure. A hit
 * refreshes the record's recency stamp so hot entries outlive the TTL sweep --
 * but only when the stored stamp is already at least `touchIntervalMs` stale, so
 * a hot entry read repeatedly isn't rewritten (full payload and all) on every
 * access (see ARTIFACT_TOUCH_INTERVAL_MS).
 */
export async function getArtifact<V>(
  ns: string,
  source: string,
  now = Date.now(),
  touchIntervalMs = ARTIFACT_TOUCH_INTERVAL_MS,
): Promise<V | undefined> {
  if (!isArtifactStoreAvailable())
    return undefined
  try {
    const db = await openDb()
    const record = await requestToPromise<ArtifactRecord | undefined>(
      db.transaction(STORE_NAME, 'readonly').objectStore(STORE_NAME).get(artifactKey(ns, source)),
    )
    // Digest collision or corruption: the stored source must match EXACTLY,
    // or the artifact belongs to some other input — a miss, never a serve.
    if (record === undefined || record.source !== source)
      return undefined
    // Touch: refresh recency so hot entries outlive the TTL sweep. Skipped while
    // the stored stamp is still fresh (< touchIntervalMs old) -- rewriting the
    // whole payload just to move `at` by minutes is pure waste against a 7-day
    // TTL. Awaited when it does run so a sweep issued after this resolves sees the
    // new stamp -- but a FAILED refresh (quota, a blocked write) must not turn
    // this valid hit into a miss, so its rejection is swallowed here and the read
    // still serves the artifact read above. (A dropped touch only risks the entry
    // aging out sooner.)
    if (now - record.at >= touchIntervalMs) {
      try {
        await putRecord(db, { ...record, at: now })
      }
      catch {
        // Recency refresh is best-effort; the artifact was already read successfully.
      }
    }
    return record.value as V
  }
  catch {
    return undefined
  }
}

/** Write (or refresh) an artifact. Best-effort; failures drop silently. */
export async function putArtifact(ns: string, source: string, value: unknown, now = Date.now()): Promise<void> {
  if (!isArtifactStoreAvailable())
    return
  try {
    const db = await openDb()
    await putRecord(db, { k: artifactKey(ns, source), source, value, at: now })
  }
  catch {
    // Quota/private-browsing failures: persistence is an optimization only.
  }
}

/**
 * Delete expired entries (TTL since last use) and, past the entry cap, the
 * oldest-used survivors. Run once per session at idle (see
 * scheduleRenderPipelineWarmup). Resolves the number of deleted entries.
 */
export async function sweepArtifacts(opts: { ttlMs?: number, maxEntries?: number, now?: number } = {}): Promise<number> {
  if (!isArtifactStoreAvailable())
    return 0
  const ttlMs = opts.ttlMs ?? ARTIFACT_TTL_MS
  const maxEntries = opts.maxEntries ?? ARTIFACT_MAX_ENTRIES
  const now = opts.now ?? Date.now()
  try {
    const db = await openDb()
    // Key-only cursor over the recency index (ascending = oldest first):
    // collect [primaryKey, at] without materializing values.
    const entries: Array<{ key: IDBValidKey, at: number }> = []
    await forEachCursor(
      db.transaction(STORE_NAME, 'readonly').objectStore(STORE_NAME).index(AT_INDEX).openKeyCursor(),
      cursor => entries.push({ key: cursor.primaryKey, at: cursor.key as number }),
    )
    // `entries` is at-ascending (the index cursor's order), which is the
    // precondition selectSweepVictims is written against. No entry is reserved
    // here: unlike the checkpoint store, this cache has no "current" row that a
    // sweep must keep. See ~/lib/idb for the shared TTL + cap arithmetic.
    const victims = selectSweepVictims(entries, { now, ttlMs, maxEntries })
    if (victims.length === 0)
      return 0
    const store = db.transaction(STORE_NAME, 'readwrite').objectStore(STORE_NAME)
    await Promise.all(victims.map(e => requestToPromise(store.delete(e.key))))
    return victims.length
  }
  catch {
    return 0
  }
}

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
 *   separately (see MARKDOWN_ARTIFACT_NS / TOKEN_ARTIFACT_NS).
 * - Output that merely gets BETTER but stays valid (e.g. tighter token
 *   merging): old artifacts still render correctly, just less optimally.
 *   Bumping anyway is a judgment call to re-render the population uniformly.
 */
export const RENDER_ARTIFACT_CACHE_VERSION = 2

/** Entries older than this are dropped by the sweep (TTL since last use). */
export const ARTIFACT_TTL_MS = 7 * 24 * 60 * 60 * 1000

/** Global entry cap across all namespaces, enforced oldest-first by the sweep. */
export const ARTIFACT_MAX_ENTRIES = 2000

const DB_NAME = 'leapmux-render-cache'
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
  return typeof indexedDB !== 'undefined'
}

/**
 * The record key. The 32-bit digest alone could collide across distinct
 * sources; the length term makes that rarer and the stored `source` check on
 * read makes it harmless (a mismatch is a miss, never a wrong artifact).
 */
function artifactKey(ns: string, source: string): string {
  return `${ns}:${fnv1a32Hex(source)}:${source.length.toString(36)}`
}

let dbPromise: Promise<IDBDatabase> | null = null

/** Visible for testing: forget the cached connection (e.g. after swapping the IDBFactory). */
export function _resetArtifactStoreForTest(): void {
  void dbPromise?.then(db => db.close()).catch(() => {})
  dbPromise = null
}

function openDb(): Promise<IDBDatabase> {
  if (!dbPromise) {
    dbPromise = new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open(DB_NAME, DB_VERSION)
      request.onupgradeneeded = () => {
        const db = request.result
        if (!db.objectStoreNames.contains(STORE_NAME)) {
          const store = db.createObjectStore(STORE_NAME, { keyPath: 'k' })
          store.createIndex(AT_INDEX, 'at')
        }
      }
      request.onsuccess = () => resolve(request.result)
      request.onerror = () => reject(request.error ?? new Error('indexedDB open failed'))
      // Blocked by another tab holding an older connection: degrade to a miss
      // now; the cached promise is dropped below so a later call retries.
      request.onblocked = () => reject(new Error('indexedDB open blocked'))
    })
    void dbPromise.catch(() => {
      dbPromise = null
    })
  }
  return dbPromise
}

function requestToPromise<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error ?? new Error('indexedDB request failed'))
  })
}

function putRecord(db: IDBDatabase, record: ArtifactRecord): Promise<void> {
  return requestToPromise(
    db.transaction(STORE_NAME, 'readwrite').objectStore(STORE_NAME).put(record),
  ).then(() => {})
}

/**
 * Read an artifact. Resolves undefined on any miss, mismatch, or failure. A hit
 * refreshes the record's recency stamp so hot entries outlive the TTL sweep.
 */
export async function getArtifact<V>(ns: string, source: string, now = Date.now()): Promise<V | undefined> {
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
    // Touch: refresh recency so hot entries outlive the TTL sweep. Awaited so
    // a sweep issued after this resolves is guaranteed to see the new stamp.
    await putRecord(db, { ...record, at: now })
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
    const entries = await new Promise<Array<{ key: IDBValidKey, at: number }>>((resolve, reject) => {
      const collected: Array<{ key: IDBValidKey, at: number }> = []
      const cursorRequest = db.transaction(STORE_NAME, 'readonly')
        .objectStore(STORE_NAME)
        .index(AT_INDEX)
        .openKeyCursor()
      cursorRequest.onsuccess = () => {
        const cursor = cursorRequest.result
        if (!cursor) {
          resolve(collected)
          return
        }
        collected.push({ key: cursor.primaryKey, at: cursor.key as number })
        cursor.continue()
      }
      cursorRequest.onerror = () => reject(cursorRequest.error ?? new Error('indexedDB cursor failed'))
    })
    const expiredCount = entries.filter(e => e.at <= now - ttlMs).length
    const freshCount = entries.length - expiredCount
    // entries is at-ascending, so the first `expiredCount + overCap` are the victims.
    const deleteCount = expiredCount + Math.max(0, freshCount - maxEntries)
    if (deleteCount === 0)
      return 0
    const store = db.transaction(STORE_NAME, 'readwrite').objectStore(STORE_NAME)
    await Promise.all(entries.slice(0, deleteCount).map(e => requestToPromise(store.delete(e.key))))
    return deleteCount
  }
  catch {
    return 0
  }
}

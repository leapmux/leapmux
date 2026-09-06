/**
 * The IndexedDB mechanism behind `~/lib/browserStorage`.
 *
 * IMPORTED BY `browserStorage.ts` AND NOTHING ELSE. The gateway stays the one
 * module a caller names, and this one stays free of key names, scopes, tiers and
 * TTL policy: it moves rows, batches writes and carries change notifications
 * between tabs. `src/test-support/storageKeysAreRegistered.test.ts` enforces the
 * single importer.
 *
 * THREE THINGS LIVE HERE.
 *
 * The DATABASE: one table keyed by the whole composed storage key, with an index
 * on the expiration so the sweep can select what to delete without reading a
 * single value.
 *
 * The WRITE-BEHIND QUEUE. The gateway's synchronous accessors cannot await a
 * commit, so a write updates the caller's view immediately and lands here to be
 * flushed. Coalescing is per key and last-write-wins, and one flush is one
 * transaction, which turns a burst -- a preference batch, three relay-id
 * allocations in a tick -- into a single commit. A write reports its own
 * durability, because `persistedSeq` acts on a mark that did not reach disk.
 *
 * The CROSS-TAB TRANSPORT. `localStorage` raised a `storage` event on every
 * write and IndexedDB raises nothing, so a BroadcastChannel carries committed
 * changes to the other tabs. It publishes AFTER the transaction commits, which
 * is what lets a receiver trust the message without re-reading.
 */
import type { Table } from 'dexie'
import type Dexie from 'dexie'
import { tryCreateBroadcastChannel } from './broadcastChannel'
import { createIdbConnection, isIndexedDbAvailable } from './idb'
import { randomUUID } from './idGenerator'
import { createLogger } from './logger'

const log = createLogger('browserStorageDb')

const DB_NAME = 'leapmux-kv'
const TABLE = 'entries'
/** Dexie names an index by its key path, so this is both. */
const EXPIRES_INDEX = 'e'

/**
 * One stored entry.
 *
 * `v` is structured-cloned rather than JSON, which is why the gateway's envelope
 * is a ROW here instead of a string: it removes a stringify and a parse from
 * every access, and the large families (`chat-row-heights:`, `local-messages:`)
 * are exactly where that cost was worst. It also widens what a value may be --
 * `NaN`, `Infinity`, a `Uint8Array` and a `Map` all round-trip, where JSON
 * flattened or dropped them.
 */
export interface KvRow {
  /** The whole composed storage key, e.g. `leapmux:u:alice:browser-prefs`. */
  k: string
  v: unknown
  /** Expiration, in epoch milliseconds. */
  e: number
}

type KvDb = Dexie & {
  [TABLE]: Table<KvRow, string>
}

const STORES = {
  [TABLE]: `k, ${EXPIRES_INDEX}`,
}

const connection = createIdbConnection<KvDb>(DB_NAME, STORES)

/**
 * Whether the environment reported that it has no IndexedDB at all, or an open
 * that never succeeded. Logged ONCE per page: a line per write would put
 * hundreds on the console for a run where nothing is wrong.
 */
let loggedUnavailable = false

/** The database, or null when persistence cannot work here. Never rejects. */
async function openKv(): Promise<KvDb | null> {
  if (!isIndexedDbAvailable())
    return null
  try {
    return await connection.open()
  }
  catch (err) {
    reportUnavailable(err)
    return null
  }
}

function reportUnavailable(err: unknown): void {
  if (loggedUnavailable)
    return
  loggedUnavailable = true
  log.debug('browser storage is unavailable; this session runs on defaults', err)
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

/**
 * A read that answers EMPTY rather than rejecting.
 *
 * Every read here is a cache lookup whose miss is already a defined outcome, and
 * the failures it can meet are not the caller's to handle: a peer tab's schema
 * repair closes the connection mid-read, and a quota or a corrupt profile fails
 * the open. Rejecting would push a rejection into `loadDraft` and its
 * neighbours, none of which can do anything but treat it as a miss anyway.
 */
async function readOrEmpty<T>(body: (db: KvDb) => Promise<T>, empty: T): Promise<T> {
  const db = await openKv()
  if (!db)
    return empty
  try {
    return await body(db)
  }
  catch (err) {
    reportUnavailable(err)
    return empty
  }
}

/** The rows for `keys` that exist, in no particular order. */
export async function readKvRows(keys: readonly string[]): Promise<KvRow[]> {
  if (keys.length === 0)
    return []
  return readOrEmpty(async db => (await db[TABLE].bulkGet([...keys]))
    .filter((row): row is KvRow => row !== undefined), [])
}

/**
 * Every row whose key starts with `prefix`.
 *
 * A bound range over the primary key, so it visits only the matching rows. The
 * gateway issues one of these per mirrored prefix family rather than one scan of
 * the whole account, which is what keeps the unbounded families off the
 * sign-in path.
 */
export async function readKvPrefix(prefix: string): Promise<KvRow[]> {
  return readOrEmpty(db => db[TABLE].where('k').startsWith(prefix).toArray(), [])
}

/** One row, or undefined. */
export async function readKvRow(key: string): Promise<KvRow | undefined> {
  return readOrEmpty<KvRow | undefined>(db => db[TABLE].get(key), undefined)
}

// ---------------------------------------------------------------------------
// The write-behind queue
// ---------------------------------------------------------------------------

/**
 * The durability of one write.
 *
 * An OBJECT rather than a bare promise, because nearly every call site ignores
 * the result and a returned promise would make each of them a floating promise.
 * The one caller that acts on it (`persistedSeq`, whose mark wedges a relay if
 * it does not survive the reload) reaches through `.durable`.
 *
 * `durable` NEVER REJECTS. A refused write is not something a caller can act on
 * -- a draft, a layout snapshot or a key pin has nowhere else to go -- so it
 * resolves false and is reported here.
 */
export interface StorageWrite {
  readonly durable: Promise<boolean>
}

/** A write that is already settled, for the paths that never reach the queue. */
export const REFUSED_WRITE: StorageWrite = { durable: Promise.resolve(false) }

export interface KvWriteOptions {
  /**
   * Tell the other tabs once this commits. Only the gateway's mirrored tier
   * sets it: no other tab holds a copy of an unmirrored key, and publishing one
   * would put a user's draft prose on a channel every tab receives.
   */
  readonly publish: boolean
  /**
   * Merge as a HIGH-WATER MARK: a smaller number never overwrites a larger one.
   *
   * For the relay sequence marks. Two tabs flushing concurrently would otherwise
   * let the slower tab's smaller mark win the row, and the next reload would
   * seed below the owner the still-live sidecar holds -- the exact wedge
   * `persistedSeq` exists to prevent, arriving through the write path.
   * IndexedDB serializes the transaction, so the compare and the write inside it
   * are atomic; localStorage could not have offered this at all.
   */
  readonly monotonic?: boolean
}

interface PendingCommon {
  readonly publish: boolean
  /**
   * Every write that this op superseded, plus this one's own.
   *
   * A FLAT LIST, not a chain of wrapped callbacks. Each superseded write must
   * settle on the one commit that stored the later value -- three relay ids
   * minted in one tick collapse to a single put of the largest and three
   * successes -- but nesting one callback inside the next retains every
   * superseded op, and its whole row, until the chain unwinds.
   */
  readonly settles: Array<(ok: boolean) => void>
}

type PendingOp
  = | (PendingCommon & { kind: 'put', row: KvRow, monotonic: boolean })
    | (PendingCommon & { kind: 'delete', key: string })

let pending = new Map<string, PendingOp>()
let flushScheduled = false
let flushInFlight: Promise<void> | null = null
/**
 * The batch `runFlush` swapped out of `pending` and has not settled yet.
 *
 * Held here because the swap happens BEFORE the first await, so a reset that
 * lands mid-flush cannot find those ops in `pending` any more -- and every one
 * of their `durable` promises would stay pending forever.
 */
let inFlightBatch: Map<string, PendingOp> | null = null
/** Bumped by `resetKvForTests`, so a flush it abandoned cannot settle or publish. */
let resetGeneration = 0
/**
 * Bumped by every enqueue, so a reader can tell that SOMETHING was written while
 * it was awaiting.
 *
 * A single counter rather than one per key: it is only ever used to decide
 * whether a read's own housekeeping is still safe to issue, and that decision
 * has to be conservative anyway. A per-key map would also grow with the key set.
 */
let writeEpoch = 0

/** Settle every write an op stands for. */
function settleOp(op: PendingOp, ok: boolean): void {
  for (const settle of op.settles)
    settle(ok)
}

/**
 * Queue `op` for `key`, superseding whatever was queued for it.
 *
 * The superseded op's durability is carried into the winner's settle list, so
 * both resolve on the one commit that stored the later value.
 */
function enqueue(
  key: string,
  build: (settles: Array<(ok: boolean) => void>, superseded: PendingOp | undefined) => PendingOp,
): StorageWrite {
  let settle!: (ok: boolean) => void
  const durable = new Promise<boolean>((resolve) => {
    settle = resolve
  })
  const superseded = pending.get(key)
  const settles = superseded === undefined ? [settle] : [...superseded.settles, settle]
  writeEpoch++
  pending.set(key, build(settles, superseded))
  scheduleFlush()
  return { durable }
}

export function enqueueKvPut(row: KvRow, opts: KvWriteOptions): StorageWrite {
  return enqueue(row.k, (settles, superseded) => ({
    kind: 'put',
    row,
    publish: opts.publish,
    // A put that supersedes a pending DELETE REPLACES the row; it does not merge
    // with it. The caller asked for the stored value to go, so the high-water
    // compare must not resurrect it.
    //
    // This is the only way a monotonic mark can ever be repaired. A stored value
    // that is not a usable mark -- `Infinity`, a fraction, a number past the
    // composition ceiling -- compares greater than every real mark, so no
    // ordinary write can replace it and the sequence stays poisoned for the life
    // of the profile. `persistedSeq` drops the row when it refuses to seed from
    // it, and that delete is what lets the mark it writes next actually land.
    monotonic: opts.monotonic === true && superseded?.kind !== 'delete',
    settles,
  }))
}

export function enqueueKvDelete(key: string, opts: KvWriteOptions): StorageWrite {
  return enqueue(key, settles => ({ kind: 'delete', key, publish: opts.publish, settles }))
}

/**
 * What is queued for `key`, so a read issued before the flush sees it.
 *
 * The synchronous tier does not need this -- the gateway's mirror already
 * answers a read-after-write in the same tick -- but the ASYNCHRONOUS tier has
 * no mirror, and two of its callers do a read-modify-write over a list. Without
 * this they would read the value the pending write is about to replace.
 */
export function peekKvPending(key: string): { row: KvRow } | { removed: true } | undefined {
  // The IN-FLIGHT batch counts too. `runFlush` swaps it out of `pending` before
  // its first await, so between that swap and the commit a write is in neither
  // the queue nor the database -- and a reader that missed it would answer with
  // the row it is about to replace.
  const op = pending.get(key) ?? inFlightBatch?.get(key)
  if (op === undefined)
    return undefined
  return op.kind === 'put' ? { row: op.row } : { removed: true }
}

/**
 * How many writes this tab has enqueued. For a reader that must know whether one
 * landed while it was awaiting.
 */
export function kvWriteEpoch(): number {
  return writeEpoch
}

/**
 * Run one flush on the next microtask.
 *
 * A microtask rather than a timer, so a burst inside one turn -- a preference
 * batch, a `batch()` of device-tier seeds -- becomes one transaction while the
 * transaction still starts in the same task window. The window in which an
 * unload loses a write is therefore one IndexedDB round trip, not a debounce
 * interval.
 */
function scheduleFlush(): void {
  if (flushScheduled || flushInFlight !== null)
    return
  flushScheduled = true
  queueMicrotask(() => {
    flushScheduled = false
    void runFlush()
  })
}

async function runFlush(): Promise<void> {
  if (flushInFlight !== null || pending.size === 0)
    return
  // Swapped out before the first await, so writes issued while this batch is in
  // flight accumulate into a fresh map and are flushed after it settles rather
  // than being lost or half-applied.
  const batch = pending
  pending = new Map()
  // Published so a reset can settle this batch. It is no longer reachable
  // through `pending`, and a write whose durability never settles hangs the
  // caller that awaited it.
  inFlightBatch = batch

  const generation = resetGeneration
  flushInFlight = (async () => {
    let landed: Set<string> | null = null
    const db = await openKv()
    // The queue was reset while this batch was opening. `resetKvForTests`
    // settled its ops and its database is not this one's, so it must not report.
    if (generation !== resetGeneration)
      return
    if (db === null) {
      // Not a refusal: the environment has none. Already reported once.
      for (const op of batch.values())
        settleOp(op, false)
      return
    }
    try {
      landed = await applyBatch(db, batch)
    }
    catch (err) {
      // A reset that lands mid-transaction closes the connection underneath it,
      // and the failure that produces says nothing about the storage: the batch
      // was abandoned on purpose.
      if (generation === resetGeneration)
        reportWriteFailure([...batch.keys()], err)
    }
    if (generation !== resetGeneration)
      return
    for (const op of batch.values())
      settleOp(op, landed !== null)
    if (landed !== null)
      publishCommitted(batch, landed)
  })()

  try {
    await flushInFlight
  }
  finally {
    if (generation === resetGeneration) {
      inFlightBatch = null
      flushInFlight = null
      if (pending.size > 0)
        scheduleFlush()
    }
  }
}

/** Apply one batch in a single transaction. Resolves the keys that actually changed. */
async function applyBatch(db: KvDb, batch: Map<string, PendingOp>): Promise<Set<string>> {
  const landed = new Set<string>()
  await db.transaction('rw', db[TABLE], async () => {
    const puts: KvRow[] = []
    const deletes: string[] = []
    for (const op of batch.values()) {
      if (op.kind === 'delete') {
        deletes.push(op.key)
        landed.add(op.key)
        continue
      }
      if (op.monotonic) {
        const existing = await db[TABLE].get(op.row.k)
        // Already at or past this mark: the stored state is what the caller
        // wanted, so the write SUCCEEDS while writing nothing. It is not
        // published either -- another tab holding the larger value must not be
        // told to move back to this one.
        if (typeof existing?.v === 'number' && typeof op.row.v === 'number' && existing.v >= op.row.v)
          continue
      }
      puts.push(op.row)
      landed.add(op.row.k)
    }
    // Deletes first: within one batch a key carries at most one op, so the two
    // lists are disjoint and the order is a matter of doing the cheap one first.
    if (deletes.length > 0)
      await db[TABLE].bulkDelete(deletes)
    if (puts.length > 0)
      await db[TABLE].bulkPut(puts)
  })
  return landed
}

/**
 * Report a batch that failed, and continue.
 *
 * A failed write is not an error a caller can act on, so it stays swallowed. But
 * a REFUSAL must not be silent: the usual cause is the storage quota, and the
 * symptom a user reports is "my preferences stop saving" with nothing anywhere
 * to point at the cause.
 */
function reportWriteFailure(keys: readonly string[], err: unknown): void {
  log.warn(`browser storage write failed for ${keys.join(', ')}; the values are not persisted`, err)
}

/** Wait for every queued write to reach disk. For `pagehide` and for tests. */
export async function flushKvWrites(): Promise<void> {
  // A loop rather than one await: settling a batch can schedule the next one,
  // and a caller that asked for durability means all of it.
  while (pending.size > 0 || flushInFlight !== null) {
    if (flushInFlight !== null)
      await flushInFlight
    else
      await runFlush()
  }
}

// ---------------------------------------------------------------------------
// The sweep
// ---------------------------------------------------------------------------

/**
 * Delete every expired row, plus -- when `checkRegistrations` is set -- every row
 * `isRegistered` rejects. Resolves the keys that were deleted, so the gateway
 * can drop them from its mirror and tell the other tabs.
 *
 * Expiry comes straight off the index and reads NO value: the walk that judged a
 * 40 KB row-height blob used to have to fetch it first.
 *
 * THE REGISTRATION PASS IS NOT ON THE HOURLY CADENCE, because it cannot be
 * bounded the way expiry can. Its predicate is a property of the KEY, and a
 * registered key is `leapmux:u:<any user id>:<name>` -- the account segment is
 * an open set, so no fixed range describes "registered" and the walk has to
 * visit every primary key in the table. That table holds the unbounded families
 * the tier split exists for, times every account that ever signed in on this
 * profile, and the walk runs inside the same `rw` transaction the write queue
 * needs. What it looks for only changes when a deploy changes the registry, so
 * the gateway runs it ONCE per page and leaves the hourly timer on the
 * index-bounded expiry arm.
 */
export async function sweepKv(
  now: number,
  isRegistered: (key: string) => boolean,
  opts: { checkRegistrations: boolean },
): Promise<string[]> {
  const db = await openKv()
  if (!db)
    return []
  const deleted: string[] = []
  try {
    await db.transaction('rw', db[TABLE], async () => {
      const expired = await db[TABLE].where(EXPIRES_INDEX).below(now).primaryKeys()
      const unregistered = opts.checkRegistrations
        ? (await db[TABLE].toCollection().primaryKeys()).filter(key => !isRegistered(key))
        : []
      // A key can be in both lists; the set is what makes the delete and the
      // reported result idempotent.
      const victims = [...new Set([...expired, ...unregistered])]
      if (victims.length === 0)
        return
      await db[TABLE].bulkDelete(victims)
      deleted.push(...victims)
    })
  }
  catch (err) {
    log.warn('browser storage sweep failed; the rows are left for the next attempt', err)
    return []
  }
  return deleted
}

// ---------------------------------------------------------------------------
// The cross-tab transport
// ---------------------------------------------------------------------------

const CHANNEL_NAME = 'leapmux:browser-storage'

/** One committed change, as another tab receives it. */
export type StorageChange
  = | { readonly k: string, readonly v: unknown, readonly e: number }
    | { readonly k: string, readonly removed: true }

/**
 * One tab's committed change set.
 *
 * `changes: null` means "re-read everything": the store was cleared or rebuilt,
 * and no list of keys would describe it. It is the `event.key === null` of the
 * `storage` event this replaced, and the same subscriber branch answers for it.
 *
 * NO SENDER IN THIS BUILD PRODUCES IT, and the receiver stays total over it
 * anyway. A BroadcastChannel carries messages between tabs that may be running
 * DIFFERENT builds -- an older tab left open, a newer one after a deploy -- so
 * a receiver that handled only the shapes its own build sends would break on
 * the first one that adds a case. Dropping the branch also drops the only
 * answer to a whole-store change, which is the case with the widest blast
 * radius.
 */
export interface StorageBroadcast {
  /** The sending tab's instance id, so a tab ignores its own echo. */
  readonly from: string
  readonly changes: readonly StorageChange[] | null
}

/**
 * This tab's identity on the channel.
 *
 * `~/lib/idGenerator`'s `randomUUID`, which is what `~/lib/crdt/clientIdentity`
 * already uses to mint its own per-tab channel id. It falls back to
 * `crypto.getRandomValues` rather than to `Math.random`, so the id stays
 * RFC 4122 shaped wherever `crypto.randomUUID` is absent.
 */
const instanceId = randomUUID()

type BroadcastListener = (changes: readonly StorageChange[] | null) => void

const broadcastListeners = new Set<BroadcastListener>()

/**
 * The channel, or null where there is none.
 *
 * `tryCreateBroadcastChannel` owns the two ways it can be absent, including the
 * embedded webviews that expose the constructor and then refuse to construct one
 * -- the same hazard `~/lib/crdt/clientIdentity` meets. Without a channel there
 * is no cross-tab sync and nothing else changes, which is a strict subset of
 * what the `storage` event gave and affects exactly one consumer.
 */
const channel: BroadcastChannel | null = tryCreateBroadcastChannel(
  CHANNEL_NAME,
  (reason, err) => log.debug(`${reason}; browser-storage changes will not cross tabs`, err),
)

if (channel !== null) {
  channel.onmessage = (event: MessageEvent<StorageBroadcast>) => {
    const message = event.data
    if (!message || message.from === instanceId)
      return
    for (const listener of broadcastListeners)
      listener(message.changes)
  }
}

/**
 * This tab's id on the channel. FOR TESTS ONLY.
 *
 * A BroadcastChannel bus is shared by every test FILE in one process, and the
 * channel name is a module constant, so a sibling file's writes arrive at any
 * listener a test opens. Matching `from` against this is what lets a case assert
 * what THIS module instance published rather than what the process did.
 */
export function kvInstanceIdForTests(): string {
  return instanceId
}

/** Run `listener` when ANOTHER tab commits a published change. Returns the unsubscribe. */
export function onKvBroadcast(listener: BroadcastListener): () => void {
  broadcastListeners.add(listener)
  return () => broadcastListeners.delete(listener)
}

/** Tell the other tabs about a set of committed deletions (the sweep's). */
export function publishKvRemovals(keys: readonly string[]): void {
  if (keys.length === 0)
    return
  channel?.postMessage({
    from: instanceId,
    changes: keys.map(k => ({ k, removed: true as const })),
  } satisfies StorageBroadcast)
}

/**
 * Publish the batch's published-tier changes, AFTER the transaction committed.
 *
 * After, not before, is what lets the receiver install the value without
 * re-reading: the row it describes is already on disk.
 *
 * THE VALUE TRAVELS IN THE MESSAGE, which inverts the rule the `storage`
 * listener followed. There the payload was the raw `{v, e}` envelope, so a field
 * read straight off `event.newValue` was always undefined and the listener had
 * to re-read through the store. Here `v` is the unwrapped value, produced by the
 * same code path that wrote the row, and the re-read alternative is an
 * asynchronous IndexedDB round trip inside an event handler -- during which the
 * receiving tab's mirror would disagree with the message that woke it.
 */
function publishCommitted(batch: Map<string, PendingOp>, landed: ReadonlySet<string>): void {
  if (channel === null)
    return
  const changes: StorageChange[] = []
  for (const op of batch.values()) {
    if (!op.publish)
      continue
    if (op.kind === 'delete') {
      if (landed.has(op.key))
        changes.push({ k: op.key, removed: true })
      continue
    }
    // A monotonic put the transaction skipped did not change the row.
    if (landed.has(op.row.k))
      changes.push({ k: op.row.k, v: op.row.v, e: op.row.e })
  }
  if (changes.length > 0)
    channel.postMessage({ from: instanceId, changes } satisfies StorageBroadcast)
}

// ---------------------------------------------------------------------------
// Test support
// ---------------------------------------------------------------------------

/**
 * Drop the queue and the cached connection. FOR TESTS ONLY.
 *
 * SYNCHRONOUS, and it ABANDONS an in-flight flush rather than awaiting it.
 * Awaiting looks tidier and is a trap: a test that installs fake timers leaves
 * IndexedDB requests that never complete, so the await would hang the NEXT
 * test's setup instead of the one that caused it. The generation counter is
 * what makes abandoning safe -- the flush cannot settle a promise or publish a
 * change once its generation is stale.
 */
export function resetKvForTests(): void {
  resetGeneration++
  // BOTH maps. `runFlush` swaps its batch out of `pending` before its first
  // await, so an abandoned flush's ops are reachable only through
  // `inFlightBatch` -- and a `durable` promise nobody settles never rejects and
  // never resolves, so a caller awaiting it hangs in the NEXT test.
  for (const op of pending.values())
    settleOp(op, false)
  for (const op of inFlightBatch?.values() ?? [])
    settleOp(op, false)
  pending = new Map()
  inFlightBatch = null
  flushScheduled = false
  flushInFlight = null
  loggedUnavailable = false
  connection.reset()
}

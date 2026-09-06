import type { StorageBroadcast, StorageChange } from '~/lib/browserStorageDb'
import { IDBFactory } from 'fake-indexeddb'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  accountStorageKey,
  flushStorageWrites,
  hydrateStorageAccount,
  KEY_BROWSER_PREFS,
  KEY_CHANNEL_RELAY_SEQ,
  localStorageGet,
  localStorageLoad,
  localStorageRemove,
  localStorageSet,
  localStorageStore,
  mirrorEntryForTests,
  onStorageChanged,
  PREFIX_EDITOR_DRAFT,
  PREFIX_FILES_SHOW_HIDDEN,
  resetBrowserStorageForTests,
  resetStorageAccountForTests,
  setStorageAccount,
  setStorageAccountForTests,
  storedKeyFor,
} from '~/lib/browserStorage'
import { enqueueKvPut, kvInstanceIdForTests, peekKvPending, readKvRow } from '~/lib/browserStorageDb'
import { TEST_USER_ID } from '~/test-support/crdtBridge'

// The IndexedDB half of the storage gateway: what the mirror loads, what the
// write queue commits, what a refused write reports, and what crosses to
// another tab.
//
// A file of its own rather than more cases in `browserStorage.test.ts`, which
// runs on fake timers for its TTL arithmetic. fake-indexeddb schedules its
// request callbacks on `setImmediate`, so a frozen clock leaves every read
// pending -- and these cases are exactly the ones that must reach a database.

const ACCOUNT = TEST_USER_ID
const OTHER = 'otheraccount'

// The worker segment is UNIQUE TO THIS FILE, and that is load-bearing rather
// than cosmetic. A BroadcastChannel bus is shared by every test file running in
// one process, so a sibling file writing the same key delivers a change
// notification here -- with a different `from`, so the echo check does not
// suppress it -- and the cross-tab cases below would count it as their own.
const WORKER = 'w-browserstoragedb'

/** A `sync`-tier key, so the mirror answers for it. */
const SYNC_KEY = `${PREFIX_FILES_SHOW_HIDDEN}${WORKER}:/repo` as const
/** An `async`-tier key, so nothing is mirrored and every read reaches the store. */
const ASYNC_KEY = `${PREFIX_EDITOR_DRAFT}${WORKER}-agent-1` as const

beforeEach(() => {
  vi.stubGlobal('indexedDB', new IDBFactory())
  resetBrowserStorageForTests()
  setStorageAccountForTests(ACCOUNT)
})

afterEach(() => {
  resetBrowserStorageForTests()
  vi.unstubAllGlobals()
})

describe('hydrateStorageAccount', () => {
  it('loads the account\'s stored rows into the mirror', async () => {
    localStorageSet(SYNC_KEY, true)
    await flushStorageWrites()

    // A fresh page: the mirror is empty until the rows are read back.
    resetBrowserStorageForTests()
    await hydrateStorageAccount(ACCOUNT)
    setStorageAccount(ACCOUNT)

    expect(localStorageGet(SYNC_KEY)).toBe(true)
  })

  it('answers a mirrored read without touching the database', async () => {
    localStorageSet(SYNC_KEY, true)
    await flushStorageWrites()
    resetBrowserStorageForTests()
    await hydrateStorageAccount(ACCOUNT)
    setStorageAccount(ACCOUNT)

    // Take the database away entirely. A synchronous read is a Map lookup, so
    // it must not notice -- that is the whole reason the tier exists.
    vi.stubGlobal('indexedDB', undefined)
    expect(localStorageGet(SYNC_KEY)).toBe(true)
  })

  // The unbounded families are the reason the tier split exists: reading them
  // here would put every draft and row-height blob on the sign-in path.
  it('does not load an unmirrored key', async () => {
    localStorageStore(ASYNC_KEY, { content: 'draft', cursor: 0 })
    await flushStorageWrites()
    resetBrowserStorageForTests()
    await hydrateStorageAccount(ACCOUNT)
    setStorageAccount(ACCOUNT)

    // Still on disk, and still readable through the asynchronous accessor...
    expect(await localStorageLoad(ASYNC_KEY)).toEqual({ content: 'draft', cursor: 0 })
    // ...but it was not pulled into the mirror. With the database gone, a
    // mirrored key would still answer and this one cannot.
    vi.stubGlobal('indexedDB', undefined)
    expect(await localStorageLoad(ASYNC_KEY)).toBeUndefined()
  })

  it('leaves another account\'s rows on disk and out of the mirror', async () => {
    setStorageAccountForTests(OTHER)
    localStorageSet(SYNC_KEY, true)
    await flushStorageWrites()

    resetBrowserStorageForTests()
    await hydrateStorageAccount(ACCOUNT)
    setStorageAccount(ACCOUNT)

    expect(localStorageGet(SYNC_KEY)).toBeUndefined()
    expect(await readKvRow(accountStorageKey(OTHER, SYNC_KEY))).toBeDefined()
  })

  it('does not install a hydration a newer one superseded', async () => {
    setStorageAccountForTests(OTHER)
    localStorageSet(SYNC_KEY, 'theirs')
    setStorageAccountForTests(ACCOUNT)
    localStorageSet(SYNC_KEY, 'mine')
    await flushStorageWrites()
    resetBrowserStorageForTests()

    // Both start; the OTHER account's finishes last. Installing it then would
    // put its rows under the incoming account's namespace.
    const stale = hydrateStorageAccount(OTHER)
    const winner = hydrateStorageAccount(ACCOUNT)
    await Promise.all([stale, winner])
    setStorageAccount(ACCOUNT)

    expect(localStorageGet(SYNC_KEY)).toBe('mine')
  })

  // `AuthContext.setUser` awaits this on EVERY call, and ten settings call sites
  // reach it through `refreshUser` for the account that is already signed in. A
  // re-read would rebuild the mirror from disk and discard whatever this tab has
  // written but not yet flushed -- and the next read-modify-write of the
  // preferences document would then merge onto the rolled-back copy.
  it('does not re-read the database for the account it already holds', async () => {
    localStorageSet(SYNC_KEY, 'first')
    await flushStorageWrites()
    resetBrowserStorageForTests()
    await hydrateStorageAccount(ACCOUNT)
    setStorageAccount(ACCOUNT)

    // A write this tab has NOT flushed. A re-read would replace it with the row
    // still on disk.
    localStorageSet(SYNC_KEY, 'not flushed yet')
    await hydrateStorageAccount(ACCOUNT)

    expect(localStorageGet(SYNC_KEY)).toBe('not flushed yet')
  })

  // The same window, reached from the other side: a write issued WHILE the
  // hydration read is in flight. `installMirror` replaces the whole map, so
  // without this the write reaches disk and vanishes from memory, and the two
  // disagree for the rest of the session.
  it('keeps a write issued while the hydration read was in flight', async () => {
    localStorageSet(SYNC_KEY, 'on disk')
    await flushStorageWrites()
    resetBrowserStorageForTests()

    const hydrating = hydrateStorageAccount(ACCOUNT)
    localStorageSet(SYNC_KEY, 'written during hydration')
    await hydrating
    setStorageAccount(ACCOUNT)

    expect(localStorageGet(SYNC_KEY)).toBe('written during hydration')
    await flushStorageWrites()
    expect(await readKvRow(storedKeyFor(SYNC_KEY)!)).toMatchObject({ v: 'written during hydration' })
  })

  // Signing in must never fail on storage. A profile with no IndexedDB, or one
  // whose open fails, runs on defaults instead.
  it('resolves with an empty mirror when there is no database at all', async () => {
    resetBrowserStorageForTests()
    vi.stubGlobal('indexedDB', undefined)

    await expect(hydrateStorageAccount(ACCOUNT)).resolves.toBeUndefined()
    expect(() => setStorageAccount(ACCOUNT)).not.toThrow()
    expect(localStorageGet(SYNC_KEY)).toBeUndefined()
  })

  // A hydration is a read, and a read is where an expired value has always been
  // noticed and removed. Installing it instead would serve it synchronously for
  // the rest of the page's life, because nothing re-checks the mirror after this.
  it('neither installs nor keeps a row that expired before it was read', async () => {
    const stored = storedKeyFor(SYNC_KEY)!
    enqueueKvPut({ k: stored, v: 'stale', e: Date.now() - 1 }, { publish: false })
    await flushStorageWrites()

    resetBrowserStorageForTests()
    await hydrateStorageAccount(ACCOUNT)

    // Checked BEFORE any read, so this is the hydration's own doing. A later
    // `localStorageGet` would refuse the value too, which is why asserting only
    // through the accessor would pass with this branch deleted.
    expect(mirrorEntryForTests(stored)).toBeUndefined()
    setStorageAccount(ACCOUNT)
    expect(localStorageGet(SYNC_KEY)).toBeUndefined()
    await flushStorageWrites()
    expect(await readKvRow(stored)).toBeUndefined()
  })

  // The `catch` inside `hydrateStorageAccount`, which no failing OPEN can reach:
  // every read below it answers empty rather than rejecting. What reaches it is
  // an environment where merely TOUCHING the global throws, which Firefox's
  // private windows did for years. A rejection here would land in
  // `AuthContext.setUser` and turn a storage fault into a failed sign-in.
  it('resolves with an empty mirror when reading the database throws', async () => {
    resetBrowserStorageForTests()
    const original = Object.getOwnPropertyDescriptor(globalThis, 'indexedDB')
    Object.defineProperty(globalThis, 'indexedDB', {
      configurable: true,
      get() {
        throw new Error('IndexedDB is disabled in this context')
      },
    })
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    try {
      await expect(hydrateStorageAccount(ACCOUNT)).resolves.toBeUndefined()
    }
    finally {
      warn.mockRestore()
      if (original)
        Object.defineProperty(globalThis, 'indexedDB', original)
      else
        Reflect.deleteProperty(globalThis, 'indexedDB')
    }

    expect(() => setStorageAccount(ACCOUNT)).not.toThrow()
    expect(localStorageGet(SYNC_KEY)).toBeUndefined()
  })

  // The invariant every synchronous reader rests on, checked at the one writer
  // rather than discovered at a random later read.
  it('is required before setStorageAccount', () => {
    resetBrowserStorageForTests()
    resetStorageAccountForTests()
    expect(() => setStorageAccount(ACCOUNT)).toThrow(/not hydrated/)
  })
})

// The tier is a COMPILE error before it is a runtime one, and `tsc --noEmit`
// runs in `bun run typecheck`, so a `@ts-expect-error` that stops erroring
// fails the build. That makes this block a real guard rather than a comment.
describe('the tier is enforced by the types', () => {
  it('refuses an asynchronous key to a synchronous accessor', () => {
    // @ts-expect-error `editor-draft:` is registered as access: 'async'.
    expect(() => localStorageGet(ASYNC_KEY)).toThrow(/localStorageLoad/)
    // @ts-expect-error same, for the writer.
    expect(() => localStorageSet(ASYNC_KEY, 'x')).toThrow(/localStorageLoad/)
  })

  it('refuses a synchronous key to an asynchronous accessor', async () => {
    // @ts-expect-error `files-show-hidden:` is registered as access: 'sync'.
    await expect(localStorageLoad(SYNC_KEY)).rejects.toThrow(/localStorageGet/)
    // @ts-expect-error same, for the writer.
    expect(() => localStorageStore(SYNC_KEY, 'x')).toThrow(/localStorageGet/)
  })

  it('refuses a name no table registers', () => {
    // @ts-expect-error nothing registers this name.
    expect(() => localStorageGet('not-a-registered-key')).toThrow(/Unknown localStorage key/)
  })
})

describe('the write queue', () => {
  it('reports a committed write as durable', async () => {
    const write = localStorageSet(SYNC_KEY, true)
    await expect(write.durable).resolves.toBe(true)
    expect(await readKvRow(storedKeyFor(SYNC_KEY)!)).toMatchObject({ v: true })
  })

  it('reports a write it cannot commit, and keeps the value readable', async () => {
    vi.stubGlobal('indexedDB', undefined)
    const write = localStorageSet(SYNC_KEY, true)
    await expect(write.durable).resolves.toBe(false)
    // Not rolled back: a refused write used to leave the caller's own copy
    // alone, and it still does.
    expect(localStorageGet(SYNC_KEY)).toBe(true)
  })

  // A burst inside one turn is one transaction. Three relay ids minted in a
  // tick must not be three commits.
  it('coalesces repeated writes to one key into a single row', async () => {
    localStorageSet(SYNC_KEY, 1)
    localStorageSet(SYNC_KEY, 2)
    const last = localStorageSet(SYNC_KEY, 3)
    await expect(last.durable).resolves.toBe(true)
    expect(await readKvRow(storedKeyFor(SYNC_KEY)!)).toMatchObject({ v: 3 })
  })

  it('settles every superseded write on the one commit that stored the later value', async () => {
    const first = localStorageSet(SYNC_KEY, 1)
    const second = localStorageSet(SYNC_KEY, 2)
    await expect(Promise.all([first.durable, second.durable])).resolves.toEqual([true, true])
  })

  it('commits a delete', async () => {
    localStorageSet(SYNC_KEY, true)
    await flushStorageWrites()
    localStorageRemove(SYNC_KEY)
    await flushStorageWrites()
    expect(await readKvRow(storedKeyFor(SYNC_KEY)!)).toBeUndefined()
  })

  // The relay marks are high-water marks, so a smaller one must never win the
  // row: the next reload would seed below the owner the live sidecar holds.
  it('refuses to let a smaller monotonic mark overwrite a larger one', async () => {
    localStorageSet(KEY_CHANNEL_RELAY_SEQ, 10)
    await flushStorageWrites()

    // A second tab's slower, smaller mark.
    const smaller = localStorageSet(KEY_CHANNEL_RELAY_SEQ, 4)
    await expect(smaller.durable).resolves.toBe(true)
    expect(await readKvRow('leapmux:channel-relay-seq')).toMatchObject({ v: 10 })
  })

  // The MIRROR obeys the same merge the transaction does. Asserting only the row
  // passed while `localStorageGet` answered 4 against a disk row of 10 -- and a
  // synchronous reader is what `persistedSeq` seeds its mark from, so the split
  // is the wedge itself: this session mints ids from a mark the disk has already
  // moved past.
  it('leaves the mirror at the larger mark when a smaller one is refused', async () => {
    localStorageSet(KEY_CHANNEL_RELAY_SEQ, 10)
    await flushStorageWrites()

    localStorageSet(KEY_CHANNEL_RELAY_SEQ, 4)
    expect(localStorageGet(KEY_CHANNEL_RELAY_SEQ)).toBe(10)
    await flushStorageWrites()
    expect(localStorageGet(KEY_CHANNEL_RELAY_SEQ)).toBe(10)
    expect(await readKvRow('leapmux:channel-relay-seq')).toMatchObject({ v: 10 })
  })

  // The expiry refresh writes the SAME ROW `localStorageSet` writes, so it has to
  // carry the same merge policy. Without it an ordinary READ of a mark whose
  // mirror sits below disk writes the smaller value back and un-fences the relay.
  it('refreshes a monotonic mark without lowering the stored row', async () => {
    // A row whose expiration is far short of the registered year, so the next
    // read decides to refresh it.
    const stored = 'leapmux:channel-relay-seq'
    enqueueKvPut({ k: stored, v: 4, e: Date.now() + 60_000 }, { publish: false })
    await flushStorageWrites()

    // A fresh page reads that row into the mirror...
    resetBrowserStorageForTests()
    await hydrateStorageAccount(ACCOUNT)
    setStorageAccount(ACCOUNT)
    expect(localStorageGet(KEY_CHANNEL_RELAY_SEQ)).toBe(4)

    // ...and another tab then commits a LARGER mark, which this tab's mirror
    // does not hear about (no BroadcastChannel in this environment reaches it).
    enqueueKvPut({ k: stored, v: 10, e: Date.now() + 60_000 }, { publish: false })
    await flushStorageWrites()

    // The refresh this read issues must not carry the mirror's smaller value
    // back onto a row that has moved past it.
    expect(localStorageGet(KEY_CHANNEL_RELAY_SEQ)).toBe(4)
    await flushStorageWrites()

    expect(await readKvRow(stored)).toMatchObject({ v: 10 })
  })

  // A stored value that is not a usable mark compares GREATER than every real
  // one, so no ordinary write could ever replace it and the sequence stayed
  // poisoned for the life of the profile. A delete is what releases it.
  it('lets a delete clear a monotonic row so a smaller mark can land', async () => {
    localStorageSet(KEY_CHANNEL_RELAY_SEQ, 10)
    await flushStorageWrites()

    localStorageRemove(KEY_CHANNEL_RELAY_SEQ)
    localStorageSet(KEY_CHANNEL_RELAY_SEQ, 1)
    await flushStorageWrites()

    expect(await readKvRow('leapmux:channel-relay-seq')).toMatchObject({ v: 1 })
    expect(localStorageGet(KEY_CHANNEL_RELAY_SEQ)).toBe(1)
  })

  it('lets a larger monotonic mark through', async () => {
    localStorageSet(KEY_CHANNEL_RELAY_SEQ, 10)
    await flushStorageWrites()
    localStorageSet(KEY_CHANNEL_RELAY_SEQ, 11)
    await flushStorageWrites()
    expect(await readKvRow('leapmux:channel-relay-seq')).toMatchObject({ v: 11 })
  })

  // A read's own housekeeping must never discard a write the caller made while
  // that read was in flight. `enqueue` supersedes by key, so the refresh put or
  // the expiry delete REPLACES the caller's row -- and the appended settle still
  // reports it durable, so nothing anywhere says the value was dropped.
  it('does not let a read\'s expiry refresh discard a write issued during it', async () => {
    const stored = accountStorageKey(ACCOUNT, ASYNC_KEY)
    // Written with an expiration well short of the registered TTL, so the read
    // below decides to refresh it.
    enqueueKvPut({ k: stored, v: { content: 'on disk', cursor: 0 }, e: Date.now() + 60_000 }, { publish: false })
    await flushStorageWrites()

    const read = localStorageLoad<{ content: string }>(ASYNC_KEY)
    localStorageStore(ASYNC_KEY, { content: 'written during the read', cursor: 1 })
    await read
    await flushStorageWrites()

    expect(await readKvRow(stored)).toMatchObject({ v: { content: 'written during the read', cursor: 1 } })
    expect(await localStorageLoad(ASYNC_KEY)).toEqual({ content: 'written during the read', cursor: 1 })
  })

  it('does not let a read\'s expiry delete discard a write issued during it', async () => {
    const stored = accountStorageKey(ACCOUNT, ASYNC_KEY)
    enqueueKvPut({ k: stored, v: { content: 'expired', cursor: 0 }, e: Date.now() - 1 }, { publish: false })
    await flushStorageWrites()

    const read = localStorageLoad(ASYNC_KEY)
    localStorageStore(ASYNC_KEY, { content: 'written during the read', cursor: 1 })
    // The read answers with the value the caller just wrote, which is the same
    // read-after-write rule the queued fast path follows -- not with the expired
    // row it happened to fetch.
    await expect(read).resolves.toEqual({ content: 'written during the read', cursor: 1 })
    await flushStorageWrites()

    // And the expiry delete this read would otherwise have issued did not take
    // the caller's write with it.
    expect(await readKvRow(stored)).toMatchObject({ v: { content: 'written during the read', cursor: 1 } })
  })

  // `runFlush` swaps its batch out of `pending` before its first await, so an
  // abandoned flush's writes are reachable only through the in-flight handle. A
  // `durable` promise nobody settles never resolves and never rejects, so a
  // caller awaiting one hangs in whatever test runs next.
  it('settles the in-flight batch when the queue is reset underneath it', async () => {
    localStorageSet(SYNC_KEY, true)
    const write = localStorageSet(KEY_CHANNEL_RELAY_SEQ, 7)
    // Let `runFlush` take the batch, then pull the queue out from under it.
    await Promise.resolve()
    resetBrowserStorageForTests()

    await expect(write.durable).resolves.toBe(false)
  })

  // The THIRD side of that fact, and the one a read cannot reach on its own: a
  // batch `runFlush` already swapped out of `pending` is in neither the queue nor
  // the database until its transaction commits. `peekKvPending` consults it, which
  // is what lets the read answer without waiting for any commit.
  it('lets an unmirrored read see a write that is mid-flush', async () => {
    localStorageStore(ASYNC_KEY, { content: 'draining', cursor: 1 })
    // One microtask: `scheduleFlush` has run, so the batch is in flight and out
    // of `pending`, and its transaction has not committed.
    await Promise.resolve()
    expect(peekKvPending(accountStorageKey(ACCOUNT, ASYNC_KEY))).toBeDefined()

    expect(await localStorageLoad(ASYNC_KEY)).toEqual({ content: 'draining', cursor: 1 })
  })

  // The asynchronous tier has no mirror, so a read issued after a write has to
  // find it either in the queue or on disk. Both sides are the same fact.
  it('lets an unmirrored read see a write that has not flushed', async () => {
    localStorageStore(ASYNC_KEY, { content: 'unflushed', cursor: 1 })
    expect(await localStorageLoad(ASYNC_KEY)).toEqual({ content: 'unflushed', cursor: 1 })
  })

  it('lets an unmirrored read see a write that has flushed', async () => {
    localStorageStore(ASYNC_KEY, { content: 'flushed', cursor: 1 })
    await flushStorageWrites()
    expect(await localStorageLoad(ASYNC_KEY)).toEqual({ content: 'flushed', cursor: 1 })
  })

  // The unmirrored tier has no `readMirror` to notice an expiration for it, so
  // the read is where an expired row is both refused and removed. Without the
  // removal a draft nobody can read would occupy the quota until the hourly
  // sweep came round.
  it('refuses an expired unmirrored row and deletes it', async () => {
    const stored = storedKeyFor(ASYNC_KEY)!
    enqueueKvPut({ k: stored, v: { content: 'stale' }, e: Date.now() - 1 }, { publish: false })
    await flushStorageWrites()
    expect(await readKvRow(stored)).toBeDefined()

    expect(await localStorageLoad(ASYNC_KEY)).toBeUndefined()
    await flushStorageWrites()
    expect(await readKvRow(stored)).toBeUndefined()
  })

  // A value read out of the queue is the object the queue is about to write, so
  // handing out the reference would let a read-modify-write mutate it underneath
  // the flush -- which is exactly what a list-shaped value does.
  it('does not hand out a reference into the pending write', async () => {
    localStorageStore(ASYNC_KEY, { content: 'original', cursor: 0 })
    const read = await localStorageLoad<{ content: string }>(ASYNC_KEY)
    read!.content = 'mutated by the caller'
    await flushStorageWrites()
    expect(await localStorageLoad(ASYNC_KEY)).toEqual({ content: 'original', cursor: 0 })
  })

  // A value read out of a Solid store carries proxies, which structured clone
  // refuses. It is ordinary data, not a mistake, so it must still store.
  it('stores a value structured clone refuses', async () => {
    const proxied = new Proxy({ nested: { count: 1 } }, {})
    localStorageSet(SYNC_KEY, proxied)
    await flushStorageWrites()
    expect(await readKvRow(storedKeyFor(SYNC_KEY)!)).toMatchObject({ v: { nested: { count: 1 } } })
  })
})

describe('cross-tab changes', () => {
  /** Deliver `changes` as a peer tab would, on the real channel. */
  /**
   * Record what THIS module instance publishes.
   *
   * The channel name is a module constant and a BroadcastChannel bus is shared
   * by every test FILE in one process, so a sibling file's sweep publishes onto
   * the same channel while this one listens -- for the same keys, since both
   * files exercise the same registry. Matching the sender is what keeps these
   * cases about this tab's own writes instead of about who runs beside them.
   */
  function recordPublished(): { seen: unknown[], stop: () => void } {
    const mine = kvInstanceIdForTests()
    const seen: unknown[] = []
    const channel = new BroadcastChannel('leapmux:browser-storage')
    channel.onmessage = (event: MessageEvent<StorageBroadcast>) => {
      if (event.data.from === mine)
        seen.push(event.data)
    }
    return { seen, stop: () => channel.close() }
  }

  /**
   * Record the change notifications that name one of `keys`.
   *
   * Same isolation problem as {@link recordPublished}, reached from the
   * receiving side: this module's own `onKvBroadcast` handler answers a sibling
   * file's publish too. A `null` set means "the whole store changed" and is
   * always kept, because every entry has to answer for it.
   */
  function recordHeard(...keys: readonly string[]): { heard: Array<ReadonlySet<string> | null>, stop: () => void } {
    const wanted = new Set(keys)
    const heard: Array<ReadonlySet<string> | null> = []
    const stop = onStorageChanged((notified) => {
      if (notified === null) {
        heard.push(null)
        return
      }
      const mine = new Set([...notified].filter(key => wanted.has(key)))
      if (mine.size > 0)
        heard.push(mine)
    })
    return { heard, stop }
  }

  async function announce(changes: readonly StorageChange[] | null, from = 'another-tab'): Promise<void> {
    const channel = new BroadcastChannel('leapmux:browser-storage')
    channel.postMessage({ from, changes })
    channel.close()
    // Delivery is a macrotask, even between two channels in one process.
    await new Promise(resolve => setTimeout(resolve, 0))
  }

  it('publishes a committed write to the other tabs', async () => {
    const { seen, stop } = recordPublished()

    localStorageSet(SYNC_KEY, true)
    await flushStorageWrites()
    await new Promise(resolve => setTimeout(resolve, 0))
    stop()

    // The VALUE travels, unwrapped. A receiver installs it without re-reading,
    // which is why the publish happens after the transaction commits.
    expect(seen).toEqual([{
      from: expect.any(String),
      changes: [{ k: storedKeyFor(SYNC_KEY), v: true, e: expect.any(Number) }],
    }])
  })

  it('does not publish an unmirrored write', async () => {
    const { seen, stop } = recordPublished()

    // No other tab holds a copy of a draft, and publishing one would put the
    // user's prose on a channel every tab receives.
    localStorageStore(ASYNC_KEY, { content: 'private', cursor: 0 })
    await flushStorageWrites()
    await new Promise(resolve => setTimeout(resolve, 0))
    stop()

    expect(seen).toEqual([])
  })

  it('applies another tab\'s change to the mirror and notifies', async () => {
    const stored = storedKeyFor(SYNC_KEY)!
    const { heard, stop } = recordHeard(stored)
    await announce([{ k: stored, v: 'from the other tab', e: Date.now() + 60_000 }])
    stop()

    expect(localStorageGet(SYNC_KEY)).toBe('from the other tab')
    expect(heard).toEqual([new Set([stored])])
  })

  it('applies another tab\'s removal', async () => {
    localStorageSet(SYNC_KEY, true)
    const stored = storedKeyFor(SYNC_KEY)!

    await announce([{ k: stored, removed: true }])

    expect(localStorageGet(SYNC_KEY)).toBeUndefined()
  })

  it('ignores its own echo', async () => {
    const { heard, stop } = recordHeard(storedKeyFor(SYNC_KEY)!)

    localStorageSet(SYNC_KEY, 'mine')
    await flushStorageWrites()
    await new Promise(resolve => setTimeout(resolve, 0))
    stop()

    // Without the sender check the two tabs would notify each other forever.
    expect(heard).toEqual([])
  })

  it('ignores a change naming another account\'s key', async () => {
    const { heard, stop } = recordHeard(accountStorageKey(OTHER, KEY_BROWSER_PREFS))

    await announce([{
      k: accountStorageKey(OTHER, KEY_BROWSER_PREFS),
      v: { diffView: 'split' },
      e: Date.now() + 60_000,
    }])
    stop()

    expect(heard).toEqual([])
  })

  it('ignores a change naming an unmirrored or unregistered key', async () => {
    const { heard, stop } = recordHeard(accountStorageKey(ACCOUNT, ASYNC_KEY), 'leapmux:not-a-registered-key')

    await announce([
      { k: accountStorageKey(ACCOUNT, ASYNC_KEY), v: 'draft', e: Date.now() + 60_000 },
      { k: 'leapmux:not-a-registered-key', v: 1, e: Date.now() + 60_000 },
    ])
    stop()

    expect(heard).toEqual([])
  })

  // Some embedded webviews expose the constructor and then refuse to construct
  // one, which `~/lib/crdt/clientIdentity` documents at length. Losing cross-tab
  // sync there is acceptable; throwing at module evaluation, which would take
  // the whole app down, is not.
  it('loads and stores with no BroadcastChannel at all', async () => {
    // A FRESH module graph, because the channel is constructed once at module
    // evaluation. The gateway this returns is a second instance of the one
    // imported at the top of the file, which is why every call below goes
    // through it rather than through the static bindings.
    vi.resetModules()
    vi.stubGlobal('BroadcastChannel', undefined)
    vi.stubGlobal('indexedDB', new IDBFactory())
    const storage = await import('~/lib/browserStorage')
    try {
      storage.setStorageAccountForTests(ACCOUNT)
      const stop = storage.onStorageChanged(() => {})
      storage.localStorageSet(SYNC_KEY, true)
      await storage.flushStorageWrites()

      expect(storage.localStorageGet(SYNC_KEY)).toBe(true)
      stop()
    }
    finally {
      storage.resetBrowserStorageForTests()
      vi.resetModules()
    }
  })

  // A clear, or a database the scaffold had to rebuild: no key list describes
  // it, so every subscriber has to answer for it.
  it('passes a whole-store change through as null', async () => {
    const heard: Array<ReadonlySet<string> | null> = []
    const stop = onStorageChanged(keys => heard.push(keys))

    await announce(null)
    stop()

    expect(heard).toEqual([null])
  })
})

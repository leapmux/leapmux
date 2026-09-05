import type { StorageChange } from '~/lib/browserStorageDb'
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
  onStorageChanged,
  PREFIX_EDITOR_DRAFT,
  PREFIX_FILES_SHOW_HIDDEN,
  resetBrowserStorageForTests,
  resetStorageAccountForTests,
  setStorageAccount,
  setStorageAccountForTests,
  storedKeyFor,
} from '~/lib/browserStorage'
import { readKvRow } from '~/lib/browserStorageDb'
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

/** A `sync`-tier key, so the mirror answers for it. */
const SYNC_KEY = `${PREFIX_FILES_SHOW_HIDDEN}w1:/repo` as const
/** An `async`-tier key, so nothing is mirrored and every read reaches the store. */
const ASYNC_KEY = `${PREFIX_EDITOR_DRAFT}agent-1` as const

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

  // Signing in must never fail on storage. A profile with no IndexedDB, or one
  // whose open fails, runs on defaults instead.
  it('resolves with an empty mirror when there is no database at all', async () => {
    resetBrowserStorageForTests()
    vi.stubGlobal('indexedDB', undefined)

    await expect(hydrateStorageAccount(ACCOUNT)).resolves.toBeUndefined()
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

  it('lets a larger monotonic mark through', async () => {
    localStorageSet(KEY_CHANNEL_RELAY_SEQ, 10)
    await flushStorageWrites()
    localStorageSet(KEY_CHANNEL_RELAY_SEQ, 11)
    await flushStorageWrites()
    expect(await readKvRow('leapmux:channel-relay-seq')).toMatchObject({ v: 11 })
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
  async function announce(changes: readonly StorageChange[] | null, from = 'another-tab'): Promise<void> {
    const channel = new BroadcastChannel('leapmux:browser-storage')
    channel.postMessage({ from, changes })
    channel.close()
    // Delivery is a macrotask, even between two channels in one process.
    await new Promise(resolve => setTimeout(resolve, 0))
  }

  it('publishes a committed write to the other tabs', async () => {
    const seen: unknown[] = []
    const channel = new BroadcastChannel('leapmux:browser-storage')
    channel.onmessage = (event: MessageEvent) => seen.push(event.data)

    localStorageSet(SYNC_KEY, true)
    await flushStorageWrites()
    await new Promise(resolve => setTimeout(resolve, 0))
    channel.close()

    // The VALUE travels, unwrapped. A receiver installs it without re-reading,
    // which is why the publish happens after the transaction commits.
    expect(seen).toEqual([{
      from: expect.any(String),
      changes: [{ k: storedKeyFor(SYNC_KEY), v: true, e: expect.any(Number) }],
    }])
  })

  it('does not publish an unmirrored write', async () => {
    const seen: unknown[] = []
    const channel = new BroadcastChannel('leapmux:browser-storage')
    channel.onmessage = (event: MessageEvent) => seen.push(event.data)

    // No other tab holds a copy of a draft, and publishing one would put the
    // user's prose on a channel every tab receives.
    localStorageStore(ASYNC_KEY, { content: 'private', cursor: 0 })
    await flushStorageWrites()
    await new Promise(resolve => setTimeout(resolve, 0))
    channel.close()

    expect(seen).toEqual([])
  })

  it('applies another tab\'s change to the mirror and notifies', async () => {
    const heard: Array<ReadonlySet<string> | null> = []
    const stop = onStorageChanged(keys => heard.push(keys))

    const stored = storedKeyFor(SYNC_KEY)!
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
    const heard: Array<ReadonlySet<string> | null> = []
    const stop = onStorageChanged(keys => heard.push(keys))

    localStorageSet(SYNC_KEY, 'mine')
    await flushStorageWrites()
    await new Promise(resolve => setTimeout(resolve, 0))
    stop()

    // Without the sender check the two tabs would notify each other forever.
    expect(heard).toEqual([])
  })

  it('ignores a change naming another account\'s key', async () => {
    const heard: Array<ReadonlySet<string> | null> = []
    const stop = onStorageChanged(keys => heard.push(keys))

    await announce([{
      k: accountStorageKey(OTHER, KEY_BROWSER_PREFS),
      v: { diffView: 'split' },
      e: Date.now() + 60_000,
    }])
    stop()

    expect(heard).toEqual([])
  })

  it('ignores a change naming an unmirrored or unregistered key', async () => {
    const heard: Array<ReadonlySet<string> | null> = []
    const stop = onStorageChanged(keys => heard.push(keys))

    await announce([
      { k: accountStorageKey(ACCOUNT, ASYNC_KEY), v: 'draft', e: Date.now() + 60_000 },
      { k: 'leapmux:not-a-registered-key', v: 1, e: Date.now() + 60_000 },
    ])
    stop()

    expect(heard).toEqual([])
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

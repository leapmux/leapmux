import type { CheckpointRecorder, CheckpointRecorderOptions } from '~/lib/crdt/checkpointRecorder'
import { create, toBinary } from '@bufbuild/protobuf'
import { IDBFactory, IDBKeyRange } from 'fake-indexeddb'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { channelManager } from '~/api/workerRpc'
import { UserCrdtStateSchema } from '~/generated/leapmux/v1/user_crdt_pb'
import { WatchUserEventSchema } from '~/generated/leapmux/v1/user_ops_pb'
import { KEY_CLIENT_ID, sessionStorageSet } from '~/lib/browserStorage'
import { PendingOpsManager } from '~/lib/crdt'
import { fullCheckpointDelta } from '~/lib/crdt/checkpointChunks'
import { CHECKPOINT_OP_LOG_THRESHOLD } from '~/lib/crdt/checkpointRecorder'
import {
  _resetCheckpointStoreForTest,
  adoptCheckpoint,
  CHECKPOINT_MAX_OWNERS,
  CHECKPOINT_TTL_MS,
  readCheckpoint,
  SEED_CANDIDATE_MAX_AGE_MS,
  writeCheckpointAndTruncateOpLog,
} from '~/lib/crdt/checkpointStore'
import { createActiveClientStore } from '~/lib/presence/activeClient'

import { CLOSE_REASON_TOO_MANY_CONNECTIONS } from '~/lib/wsCloseCodes'
import { createOpLogAppender } from '~/test-support/opLog'
import { useCrdtRuntime } from './useCrdtRuntime'

// Partially mocked so ONE case can force the sibling adoption to fail; every
// other export, adoptCheckpoint included, stays the real implementation.
vi.mock('~/lib/crdt/checkpointStore', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/crdt/checkpointStore')>()
  return { ...actual, adoptCheckpoint: vi.fn(actual.adoptCheckpoint) }
})

const opLog = createOpLogAppender()

// useUserEvents opens a socket the moment its ready gate flips, which this
// suite has no business driving -- stub it out and expose the gate so the tests
// can observe exactly what `hydrated` does.
const userEventsSpy = vi.hoisted(() => ({
  ready: null as null | (() => boolean),
  // Captured so a test can fire a terminal close and assert WHICH message the
  // runtime chose. The hook itself already carries the close reason to here;
  // what is worth pinning is that this end reads it.
  onFatalClose: null as null | ((info: { code: number, reason: string }) => void),
}))
vi.mock('./useUserEvents', () => ({
  useUserEvents: (opts: {
    ready?: () => boolean
    onFatalClose?: (info: { code: number, reason: string }) => void
  }) => {
    userEventsSpy.ready = opts.ready ?? (() => true)
    userEventsSpy.onFatalClose = opts.onFatalClose ?? null
    return { bootstrapped: () => false, clock: () => null, reconnect: () => {}, relayId: () => 0 }
  },
  nextUserEventsRelayId: () => 1,
}))

// The toast is the only surface a fatal close has; assert the text that reaches
// it rather than that something was called.
const toastSpy = vi.hoisted(() => ({ sticky: [] as string[], warn: [] as string[] }))
vi.mock('~/components/common/Toast', () => ({
  showStickyWarnToast: (m: string) => { toastSpy.sticky.push(m) },
  showWarnToast: (m: string) => { toastSpy.warn.push(m) },
  showInfoToast: () => {},
  showErrorToast: () => {},
}))

// The recorder is constructed deep inside the hook and never returned, but WHAT
// it is constructed with is load-bearing: the frames hydration replayed seed
// both its op-log counter and its dirty set. Wrapping the real factory keeps
// every assertion about real behaviour -- the recorder here is the production
// one -- while making the seeding observable.
const recorderSpy = vi.hoisted(() => ({
  options: [] as CheckpointRecorderOptions[],
  instances: [] as CheckpointRecorder[],
  failNextConstruction: false,
}))
vi.mock('~/lib/crdt/checkpointRecorder', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/crdt/checkpointRecorder')>()
  return {
    ...actual,
    createCheckpointRecorder: (opts: CheckpointRecorderOptions) => {
      if (recorderSpy.failNextConstruction) {
        recorderSpy.failNextConstruction = false
        throw new Error('recorder construction blew up')
      }
      recorderSpy.options.push(opts)
      const recorder = actual.createCheckpointRecorder(opts)
      recorderSpy.instances.push(recorder)
      return recorder
    },
  }
})

const hydrateSpy = vi.hoisted(() => ({
  throwOnNextRead: false,
  hangNextRead: false,
  // The options the runtime actually handed the read, so a test can invoke the
  // supersession predicate itself. Recorded even on the hang path, which is the
  // only one that reaches the deadline.
  lastOpts: undefined as { superseded?: () => boolean } | undefined,
}))
vi.mock('~/lib/crdt/hydrate', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/crdt/hydrate')>()
  return {
    ...actual,
    // Signature MIRRORED via Parameters<>, not restated. A hand-written
    // (userId, clientId) pair silently DROPPED the options argument the moment
    // loadHydrationState grew one -- which would have disabled the seed's
    // supersession guard in every test in this file while they all still passed.
    loadHydrationState: async (...args: Parameters<typeof actual.loadHydrationState>) => {
      hydrateSpy.lastOpts = args[2]
      if (hydrateSpy.throwOnNextRead) {
        hydrateSpy.throwOnNextRead = false
        throw new Error('hydration read blew up')
      }
      if (hydrateSpy.hangNextRead) {
        hydrateSpy.hangNextRead = false
        // Never settles -- the one shape a try/catch cannot see.
        return new Promise(() => {})
      }
      return actual.loadHydrationState(...args)
    },
  }
})

/**
 * Settle the hydration chain. `loadHydrationState` awaits IndexedDB, so the
 * effect's async completion lands several microtasks after the signal write;
 * a couple of macrotask turns is comfortably enough and keeps the test honest
 * about the fact that hydration is NOT synchronous.
 */
async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++)
    await new Promise(resolve => setTimeout(resolve, 0))
}

/**
 * The supersession predicate the runtime actually handed `loadHydrationState`,
 * failing loudly if it handed none.
 *
 * A function rather than a direct `hydrateSpy.lastOpts` read so the caller gets
 * the DECLARED type: reading the property in a test body that also assigns to it
 * lets control-flow analysis narrow it to `undefined`.
 */
function handedSupersessionGuard(): () => boolean {
  const opts = hydrateSpy.lastOpts
  if (!opts?.superseded)
    throw new Error('loadHydrationState was called without a supersession guard')
  return opts.superseded
}

/**
 * Settle until `until` holds, or give up after a bounded number of turns.
 *
 * Positive assertions ("the gate opened") must poll rather than take a fixed
 * number of turns: the hydration chain is several awaits deep and grew again
 * when the abandoned-checkpoint sweep and the client-id handshake joined it, so
 * a fixed count that is comfortable in isolation goes flaky under a loaded full
 * suite. Negative assertions ("the gate stayed shut") keep using `settle`, since
 * polling for something that must never happen would only ever burn its budget.
 */
async function settleUntil(until: () => boolean): Promise<void> {
  for (let i = 0; i < 200 && !until(); i++)
    await new Promise(resolve => setTimeout(resolve, 0))
}

/**
 * settleUntil for a predicate that must AWAIT (an IDB read, say). Separate
 * rather than widening settleUntil's type: a sync predicate accidentally passed
 * to an async-aware loop would be awaited as a non-promise and pass on its
 * first truthy value, which is the same shape of bug in reverse.
 */
async function settleUntilAsync(until: () => Promise<boolean>): Promise<void> {
  for (let i = 0; i < 200 && !(await until()); i++)
    await new Promise(resolve => setTimeout(resolve, 0))
}

function mountRuntime(userId: () => string) {
  let dispose = () => {}
  const runtime = createRoot((d) => {
    dispose = d
    return useCrdtRuntime({
      userId,
      getWorkspaceId: () => 'ws-1',
      activeClient: createActiveClientStore(),
      onWorkspaceLifecycleChanged: () => {},
    })
  })
  return { runtime, dispose }
}

/**
 * The checkpoint is keyed by (user, CLIENT id), and the runtime derives its
 * client id from sessionStorage. Pinning it here makes the seeded row
 * addressable; without it the runtime would mint a fresh id and read a miss.
 */
const CLIENT = 'c-test-tab'
const WM = { physical: 10n, logical: 0n, clientId: 'c' }

/** A checkpoint whose state carries `ws-1`, so a replay is distinguishable. */
function seededState(userId: string) {
  return create(UserCrdtStateSchema, {
    userId,
    workspaces: { 'ws-1': { workspaceId: 'ws-1' } },
    maxHlc: WM,
    currentEpoch: 1n,
  })
}

function seedCheckpoint(userId: string): Promise<boolean> {
  return writeCheckpointAndTruncateOpLog(userId, CLIENT, fullCheckpointDelta(seededState(userId)), WM, 1n)
}

/** A confirmed frame naming one node, so its entity key is observable. */
function nodeFrameBytes(batchId: string, nodeId: string): Uint8Array {
  return toBinary(WatchUserEventSchema, create(WatchUserEventSchema, {
    event: {
      case: 'batch',
      value: { batchId, ops: [{ body: { case: 'setNodeRegister', value: { nodeId } } }] },
    },
  } as never))
}

async function opLogFrameCount(userId: string): Promise<number> {
  const read = await readCheckpoint(userId, CLIENT)
  return read.status === 'ok' ? read.opLogFrames.length : -1
}

describe('useCrdtRuntime hydration gate', () => {
  beforeEach(() => {
    // WITHOUT this stub the whole suite is a tautology. jsdom implements no
    // IndexedDB, so `isCheckpointStoreAvailable()` is false: every
    // `writeCheckpointAndTruncateOpLog` here would write nothing and
    // `loadHydrationState` would return null on its first line. The case named
    // "opens the ready gate when a persisted checkpoint IS replayed" then
    // exercised byte-for-byte the same `payload === null` path as its
    // no-checkpoint sibling, leaving the hydrate branch, the hydrate-throw
    // fallback and the op-log-count seeding entirely uncovered -- the suite
    // stayed green with `if (payload)` deleted outright.
    vi.stubGlobal('indexedDB', new IDBFactory())
    vi.stubGlobal('IDBKeyRange', IDBKeyRange)
    sessionStorageSet(KEY_CLIENT_ID, CLIENT)
    _resetCheckpointStoreForTest()
    opLog.reset()
    userEventsSpy.ready = null
    recorderSpy.options = []
    recorderSpy.instances = []
    recorderSpy.failNextConstruction = false
    hydrateSpy.throwOnNextRead = false
    hydrateSpy.hangNextRead = false
  })

  afterEach(() => {
    _resetCheckpointStoreForTest()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  // Regression guard for a hydration-invalidation token compared against the
  // WRONG counter. The bug is invisible to every other suite: the runtime
  // reports no error, it simply never marks itself hydrated, so the ready gate
  // stays shut and `/ws/userevents` is never dialled. The app looks like it is
  // "still loading" forever.
  it('opens the ready gate after a first-login hydration with no persisted checkpoint', async () => {
    const { dispose } = mountRuntime(() => 'user-a')
    expect(userEventsSpy.ready).not.toBeNull()
    // Gate starts shut: the socket must not race the checkpoint replay.
    expect(userEventsSpy.ready!()).toBe(false)

    await settleUntil(() => userEventsSpy.ready!())

    expect(userEventsSpy.ready!()).toBe(true)
    // Nothing persisted, so the recorder has no base to be incremental against.
    expect(recorderSpy.options.at(-1)!.hydratedFrom).toBeUndefined()
    dispose()
  })

  it('opens the ready gate when a persisted checkpoint IS replayed', async () => {
    // Seed a checkpoint so hydration takes the non-null payload branch. The
    // state blob carries a workspace so the hydrated result is DISTINGUISHABLE
    // from the cold-start path: `ready()` alone is true either way, so asserting
    // only the gate would pass even if the whole `if (payload)` branch were
    // deleted -- which is exactly what this suite used to do.
    await seedCheckpoint('user-b')
    const { runtime, dispose } = mountRuntime(() => 'user-b')
    await settleUntil(() => userEventsSpy.ready!())

    expect(userEventsSpy.ready!()).toBe(true)
    // The checkpoint's state really was installed as the confirmed base.
    const state = runtime.crdtState()
    expect(state).not.toBeNull()
    expect(Object.keys(state!.workspaces)).toContain('ws-1')
    dispose()
  })

  // A direct A -> B switch (no intervening logout) is a supported transition:
  // refreshUser() replaces the user in place and AuthGuard deliberately does not
  // remount the shell. The hydration token must survive it -- capturing the
  // token before the reset that bumps it made this case invalidate its own
  // hydration every time, wedging the gate shut for the incoming account.
  it('re-opens the ready gate across a direct user switch', async () => {
    const [uid, setUid] = createSignal('user-a')
    const { dispose } = mountRuntime(uid)
    await settleUntil(() => userEventsSpy.ready!())
    expect(userEventsSpy.ready!()).toBe(true)

    setUid('user-b')
    // The gate shuts immediately so the socket cannot open against the previous
    // account's state...
    expect(userEventsSpy.ready!()).toBe(false)
    await settleUntil(() => userEventsSpy.ready!())
    // ...and re-opens once the incoming account's hydration installs.
    expect(userEventsSpy.ready!()).toBe(true)
    dispose()
  })

  // Disposal is a cancellation path the old generation counter never covered.
  // It bumped only in the `!uid` branch and the uid-switch branch, so a teardown
  // that leaves `auth.user()` intact -- the desktop disconnect that unmounts the
  // whole connected branch, or the route ErrorBoundary disposing the AppShell
  // subtree -- let the in-flight hydration run to completion and install a live
  // manager plus a CheckpointRecorder into a tree the app had already thrown
  // away, with nothing left to dispose it. Solid runs a computation's cleanups
  // on owner disposal as well as before each re-run, which is why the flag it
  // replaces covers this for free.
  it('abandons an in-flight hydration when its owner is disposed', async () => {
    const { dispose } = mountRuntime(() => 'user-a')
    expect(userEventsSpy.ready!()).toBe(false)

    // Tear down BEFORE hydration settles, with the user id still set.
    dispose()
    await settle()

    expect(userEventsSpy.ready!()).toBe(false)
  })

  // A read that never settles is the ONE shape the surrounding try/catch cannot
  // see, and this await is the only thing holding the ready gate shut --
  // `useUserEvents` returns without connecting AND without scheduling a
  // reconnect while it is closed. So a hung `indexedDB.open` (a real condition:
  // WebKit IDB hangs, a corrupt profile, a wedged versionchange in a peer
  // window) left an empty shell with no CRDT state, no presence, no error UI
  // and no recovery but a manual reload. Losing the race must cost one full
  // snapshot, not the session.
  it('opens the ready gate when the hydration read never settles', async () => {
    vi.useFakeTimers()
    try {
      hydrateSpy.hangNextRead = true
      const { dispose } = mountRuntime(() => 'user-hang')
      await vi.advanceTimersByTimeAsync(0)
      expect(userEventsSpy.ready!()).toBe(false)

      // Past the watchdog deadline.
      await vi.advanceTimersByTimeAsync(10_001)
      expect(userEventsSpy.ready!()).toBe(true)
      dispose()
    }
    finally {
      vi.useRealTimers()
    }
  })

  // The WIRING of the supersession guard, which nothing else covers.
  //
  // hydrate.test.ts proves the option's own behaviour (superseded() true => no
  // write). What it cannot see is whether this file ever passes one, or whether
  // the thing that is supposed to flip it on the deadline is connected: every
  // other test here runs with cancelled === false and deadlineMissed === false,
  // so deleting `onTimeout?.()` from withTimeout, dropping its third argument,
  // or removing `superseded:` from the call left all of them green -- while
  // reopening the hazard the guard exists for. A late adoption landing after the
  // cold start has rewritten the checkpoint leaves a hole in the op-log whose
  // replayed batchEnd frames advance the resume cursor straight past it.
  it('flips the supersession guard it handed the read once the deadline wins', async () => {
    vi.useFakeTimers()
    try {
      hydrateSpy.hangNextRead = true
      const { dispose } = mountRuntime(() => 'user-superseded')
      await vi.advanceTimersByTimeAsync(0)

      const superseded = handedSupersessionGuard()
      expect(superseded(), 'nothing has superseded the run yet').toBe(false)

      // Past the watchdog deadline: the run is abandoned but NOT cancelled, so
      // the predicate is the only thing standing between a late seed and a
      // checkpoint the cold start has already rewritten.
      await vi.advanceTimersByTimeAsync(10_001)
      expect(superseded(), 'the deadline must mark the run superseded').toBe(true)
      dispose()
    }
    finally {
      vi.useRealTimers()
    }
  })

  // The other supersession cause: the effect re-running or the owner being
  // disposed, which must mark the in-flight read superseded even though no
  // deadline fired.
  it('flips the supersession guard when the run is disposed mid-read', async () => {
    hydrateSpy.hangNextRead = true
    const { dispose } = mountRuntime(() => 'user-disposed')
    await settle()

    const superseded = handedSupersessionGuard()
    expect(superseded()).toBe(false)

    dispose()
    expect(superseded(), 'disposal must mark the run superseded').toBe(true)
  })

  it('keeps the ready gate shut with no user id', async () => {
    const { dispose } = mountRuntime(() => '')
    await settle()
    expect(userEventsSpy.ready!()).toBe(false)
    dispose()
  })

  // The owner-liveness interval is armed AFTER `await loadHydrationState`, so
  // its teardown cannot be registered there: Solid restores the previous Owner
  // before an async continuation resumes, and `onCleanup` with a null Owner is
  // silently dropped -- no throw, and only the dev build warns.
  //
  // The leak is not merely an idle timer. `touchOwner` keeps stamping
  // `lastSeenAt` under the id this run hydrated from, and that timestamp is the
  // ONLY liveness signal `sweepAbandonedCheckpoints` consults, so an abandoned
  // owner's checkpoint, entity chunks and op-log segments become permanently
  // exempt from both the TTL arm and the cap arm -- storage grows without bound
  // and every reconnect eventually degrades to the full snapshot this branch
  // exists to avoid.
  it('clears the owner-liveness interval on dispose', async () => {
    const setSpy = vi.spyOn(globalThis, 'setInterval')
    const clearSpy = vi.spyOn(globalThis, 'clearInterval')
    try {
      const { dispose } = mountRuntime(() => 'user-touch')
      await settleUntil(() => userEventsSpy.ready!() === true)

      const armed = setSpy.mock.results.map(r => r.value)
      expect(armed.length).toBeGreaterThan(0)

      dispose()

      const cleared = clearSpy.mock.calls.map(c => c[0])
      for (const handle of armed)
        expect(cleared).toContain(handle)
    }
    finally {
      setSpy.mockRestore()
      clearSpy.mockRestore()
    }
  })

  // Same defect, observed as accumulation: each effect re-run must leave at most
  // one live interval behind. A dropped cleanup adds one per re-run for the life
  // of the page, and each keeps a DIFFERENT stale owner key alive.
  it('does not accumulate liveness intervals across effect re-runs', async () => {
    const setSpy = vi.spyOn(globalThis, 'setInterval')
    const clearSpy = vi.spyOn(globalThis, 'clearInterval')
    try {
      const [uid, setUid] = createSignal('user-touch-1')
      let dispose = () => {}
      createRoot((d) => {
        dispose = d
        return useCrdtRuntime({
          userId: uid,
          getWorkspaceId: () => 'ws-1',
          activeClient: createActiveClientStore(),
          onWorkspaceLifecycleChanged: () => {},
        })
      })
      await settleUntil(() => userEventsSpy.ready!() === true)

      setUid('user-touch-2')
      await settleUntil(() => userEventsSpy.ready!() === true)
      dispose()

      const armed = setSpy.mock.results.map(r => r.value)
      const cleared = new Set(clearSpy.mock.calls.map(c => c[0]))
      const leaked = armed.filter(h => !cleared.has(h))
      expect(leaked).toEqual([])
    }
    finally {
      setSpy.mockRestore()
      clearSpy.mockRestore()
    }
  })
})

// A hydrate() that throws leaves the manager in an indeterminate state, so the
// runtime must discard it, wipe the poison pair (OWNER-scoped -- a user-wide
// wipe would cold-start every other tab as collateral) and cold-start. Nothing
// else in the suite reached this arm.
describe('useCrdtRuntime when hydrate() throws', () => {
  beforeEach(() => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    vi.stubGlobal('IDBKeyRange', IDBKeyRange)
    sessionStorageSet(KEY_CLIENT_ID, CLIENT)
    _resetCheckpointStoreForTest()
    userEventsSpy.ready = null
    recorderSpy.options = []
    recorderSpy.instances = []
    recorderSpy.failNextConstruction = false
    hydrateSpy.throwOnNextRead = false
    hydrateSpy.hangNextRead = false
  })
  afterEach(() => {
    _resetCheckpointStoreForTest()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('falls back to an EMPTY manager and opens the ready gate', async () => {
    await seedCheckpoint('user-c')
    vi.spyOn(PendingOpsManager.prototype, 'hydrate').mockImplementationOnce(() => {
      throw new Error('applyOp rejected a replayed frame')
    })

    const { runtime, dispose } = mountRuntime(() => 'user-c')
    await settleUntil(() => userEventsSpy.ready!())

    expect(userEventsSpy.ready!()).toBe(true)
    // Empty, not the seeded state: the half-hydrated manager was discarded.
    expect(Object.keys(runtime.crdtState()!.workspaces)).toEqual([])
    // ...and the fallback recorder was told it has NO base, so its first
    // rewrite is full.
    expect(recorderSpy.options.at(-1)!.hydratedFrom).toBeUndefined()
    dispose()
  })

  it('wipes the poison checkpoint so the next reload is a clean miss', async () => {
    await seedCheckpoint('user-c')
    vi.spyOn(PendingOpsManager.prototype, 'hydrate').mockImplementationOnce(() => {
      throw new Error('applyOp rejected a replayed frame')
    })

    const { dispose } = mountRuntime(() => 'user-c')
    await settleUntil(() => userEventsSpy.ready!())
    await settle()

    expect((await readCheckpoint('user-c', CLIENT)).status).toBe('miss')
    dispose()
  })
})

// The frames hydration replayed are already ON DISK -- only a checkpoint
// rewrite truncates the log -- and they are exactly what moved the state past
// the persisted chunks. So they seed BOTH the recorder's frame counter (which
// is what eventually drains a log grown by short delta-resumed sessions) and
// its dirty set (which is what the next incremental rewrite works from).
describe('useCrdtRuntime recorder seeding', () => {
  beforeEach(() => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    vi.stubGlobal('IDBKeyRange', IDBKeyRange)
    sessionStorageSet(KEY_CLIENT_ID, CLIENT)
    _resetCheckpointStoreForTest()
    userEventsSpy.ready = null
    recorderSpy.options = []
    recorderSpy.instances = []
    recorderSpy.failNextConstruction = false
    hydrateSpy.throwOnNextRead = false
    hydrateSpy.hangNextRead = false
  })
  afterEach(() => {
    _resetCheckpointStoreForTest()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('seeds the recorder\'s op-log counter and dirty set from the replayed frames', async () => {
    await seedCheckpoint('user-d')
    await opLog.append('user-d', CLIENT, [
      nodeFrameBytes('b1', 'replayed-node-1'),
      nodeFrameBytes('b2', 'replayed-node-2'),
    ])

    const { dispose } = mountRuntime(() => 'user-d')
    await settleUntil(() => userEventsSpy.ready!())

    const recorder = recorderSpy.instances.at(-1)!
    // Awaited because the option is a value on the SELF path and a promise on
    // the seed path; `await` reads both.
    expect((await recorderSpy.options.at(-1)!.hydratedFrom)!.frames).toHaveLength(2)
    // Counted, so the threshold measures the WHOLE persisted log, not just
    // post-hydration appends.
    expect(recorder.opLogCount).toBe(2)
    // ...and dirty, so the next rewrite re-serializes exactly those chunks.
    expect([...recorder.dirtyKeys].sort()).toEqual(['node:replayed-node-1', 'node:replayed-node-2'])
    expect(recorder.needsFullRewrite).toBe(false)
    dispose()
  })
})

// `truncated` means the persisted log stops short -- a poison frame, an
// unreadable segment, or the store's own frame/byte cap. Without an immediate
// rewrite the same prefix is replayed and the same stop is hit on EVERY future
// reload, and the log never drains.
describe('useCrdtRuntime post-hydration compaction', () => {
  beforeEach(() => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    vi.stubGlobal('IDBKeyRange', IDBKeyRange)
    sessionStorageSet(KEY_CLIENT_ID, CLIENT)
    _resetCheckpointStoreForTest()
    userEventsSpy.ready = null
    recorderSpy.options = []
    recorderSpy.instances = []
    recorderSpy.failNextConstruction = false
    hydrateSpy.throwOnNextRead = false
    hydrateSpy.hangNextRead = false
  })
  afterEach(() => {
    _resetCheckpointStoreForTest()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('rewrites immediately when the replay was TRUNCATED', async () => {
    await seedCheckpoint('user-e')
    await opLog.append('user-e', CLIENT, [nodeFrameBytes('good', 'n1')])
    // A frame that cannot decode: hydration replays the prefix and reports
    // truncated, leaving the poison frame on disk.
    await opLog.append('user-e', CLIENT, [new Uint8Array([0xFF, 0xFE, 0xFD])])
    expect(await opLogFrameCount('user-e')).toBe(2)

    const { dispose } = mountRuntime(() => 'user-e')
    await settleUntil(() => userEventsSpy.ready!())
    await settle()

    // The rewrite pinned a fresh checkpoint and dropped the whole log with it.
    expect(await opLogFrameCount('user-e')).toBe(0)
    dispose()
  })

  it('rewrites immediately when the replayed log is already at the threshold', async () => {
    await seedCheckpoint('user-f')
    await opLog.append(
      'user-f',
      CLIENT,
      Array.from({ length: CHECKPOINT_OP_LOG_THRESHOLD }, (_, i) => nodeFrameBytes(`b${i}`, `n${i}`)),
    )
    expect(await opLogFrameCount('user-f')).toBe(CHECKPOINT_OP_LOG_THRESHOLD)

    const { dispose } = mountRuntime(() => 'user-f')
    await settleUntil(() => userEventsSpy.ready!())
    await settle()

    expect(await opLogFrameCount('user-f')).toBe(0)
    dispose()
  })

  it('does NOT rewrite when the replayed log is short and clean', async () => {
    // The counterweight: a routine delta-resumed reload must not pay a
    // checkpoint write it does not need.
    await seedCheckpoint('user-g')
    await opLog.append('user-g', CLIENT, [nodeFrameBytes('b1', 'n1')])

    const { dispose } = mountRuntime(() => 'user-g')
    await settleUntil(() => userEventsSpy.ready!())
    await settle()

    expect(await opLogFrameCount('user-g')).toBe(1)
    dispose()
  })
})

// REMOVALS-4. `setHydrated(true)` has exactly ONE call site, and useUserEvents
// returns before it connects OR schedules a reconnect while the gate is shut.
// So a throw anywhere in the hydration chain used to leave the user with an
// empty shell, no error UI, and no recovery short of a manual reload -- a
// failure mode that did not exist before the manager was constructed
// asynchronously.
describe('useCrdtRuntime when the hydration chain throws', () => {
  beforeEach(() => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    vi.stubGlobal('IDBKeyRange', IDBKeyRange)
    sessionStorageSet(KEY_CLIENT_ID, CLIENT)
    _resetCheckpointStoreForTest()
    userEventsSpy.ready = null
    recorderSpy.options = []
    recorderSpy.instances = []
    recorderSpy.failNextConstruction = false
    hydrateSpy.throwOnNextRead = false
    hydrateSpy.hangNextRead = false
  })
  afterEach(() => {
    _resetCheckpointStoreForTest()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('still opens the ready gate when the checkpoint READ throws', async () => {
    // loadHydrationState is written never to throw. If it ever does, that is a
    // reason to cold-start -- not a reason to wedge the app.
    hydrateSpy.throwOnNextRead = true

    const { runtime, dispose } = mountRuntime(() => 'user-h')
    await settleUntil(() => userEventsSpy.ready!())

    expect(userEventsSpy.ready!()).toBe(true)
    expect(runtime.crdtState()).not.toBeNull()
    dispose()
  })

  it('still opens the ready gate when the recorder cannot be constructed', async () => {
    // The deepest arm: installObserverAndManager itself throws, so even the
    // "publish a manager" step failed. The fallback re-runs it and lands on the
    // cold-start path the module already treats as correct.
    recorderSpy.failNextConstruction = true

    const { runtime, dispose } = mountRuntime(() => 'user-i')
    await settleUntil(() => userEventsSpy.ready!())

    expect(userEventsSpy.ready!()).toBe(true)
    expect(runtime.crdtState()).not.toBeNull()
    dispose()
  })
})

// Seeding a NEW tab from a sibling's checkpoint. The runtime's client id is
// pinned to CLIENT, so a row written under any OTHER id is a sibling's.
describe('useCrdtRuntime sibling seeding', () => {
  beforeEach(() => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    vi.stubGlobal('IDBKeyRange', IDBKeyRange)
    sessionStorageSet(KEY_CLIENT_ID, CLIENT)
    _resetCheckpointStoreForTest()
    userEventsSpy.ready = null
    recorderSpy.options = []
    recorderSpy.instances = []
    recorderSpy.failNextConstruction = false
    hydrateSpy.throwOnNextRead = false
    hydrateSpy.hangNextRead = false
  })
  afterEach(() => {
    _resetCheckpointStoreForTest()
    vi.unstubAllGlobals()
  })

  /** Seed a sibling owner (a client id that is NOT this tab's). */
  function seedSibling(userId: string, clientId: string, lastSeenAt?: number): Promise<boolean> {
    return writeCheckpointAndTruncateOpLog(
      userId,
      clientId,
      fullCheckpointDelta(seededState(userId)),
      WM,
      1n,
      lastSeenAt,
    )
  }

  it('opens the ready gate with the sibling\'s state when this tab has no checkpoint', async () => {
    await seedSibling('user-1', 'c-other-tab')
    const { runtime, dispose } = mountRuntime(() => 'user-1')
    try {
      await settleUntil(() => userEventsSpy.ready!())
      expect(userEventsSpy.ready!()).toBe(true)
      // Asserting on the STATE, not just the gate: the gate opens on every cold
      // start too, so it alone cannot tell a seed from a full-snapshot start.
      expect(runtime.crdtState()!.workspaces['ws-1']).toBeDefined()
    }
    finally {
      dispose()
    }
  })

  it('writes the copy under THIS tab\'s client id and leaves the sibling\'s row', async () => {
    await seedSibling('user-1', 'c-other-tab')
    const before = await readCheckpoint('user-1', 'c-other-tab')
    const { dispose } = mountRuntime(() => 'user-1')
    try {
      await settleUntil(() => userEventsSpy.ready!())
      await settleUntilAsync(async () => (await readCheckpoint('user-1', CLIENT)).status === 'ok')

      expect((await readCheckpoint('user-1', CLIENT)).status).toBe('ok')
      expect(await readCheckpoint('user-1', 'c-other-tab')).toEqual(before)
    }
    finally {
      dispose()
    }
  })

  it('hands the recorder the seeded base, so its first rewrite is INCREMENTAL', async () => {
    await seedSibling('user-1', 'c-other-tab')
    const { dispose } = mountRuntime(() => 'user-1')
    try {
      await settleUntil(() => userEventsSpy.ready!())
      expect(recorderSpy.options.at(-1)!.clientId).toBe(CLIENT)

      // The ready gate does NOT wait for the adoption -- that is the point of
      // handing the recorder a promise -- so settle until the base lands before
      // asking what the recorder made of it.
      await recorderSpy.options.at(-1)!.hydratedFrom
      await settleUntil(() => !recorderSpy.instances.at(-1)!.needsFullRewrite)
      expect(recorderSpy.instances.at(-1)!.needsFullRewrite).toBe(false)
    }
    finally {
      dispose()
    }
  })

  // THE WIN, pinned: the /ws/userevents connect is gated on `ready`, and `ready`
  // must not wait out an adoption that writes one row per entity. Before the
  // recorder learned to hold its own appends, hydrateAndInstall awaited the
  // whole adopt transaction before opening the gate.
  it('opens the ready gate while the adoption is still committing', async () => {
    await seedSibling('user-1', 'c-other-tab')
    let resolveAdopt: (nextOrdinal: number | null) => void = () => {}
    vi.mocked(adoptCheckpoint).mockImplementationOnce(
      () => new Promise<number | null>((resolve) => { resolveAdopt = resolve }),
    )
    const { dispose } = mountRuntime(() => 'user-1')
    try {
      await settleUntil(() => userEventsSpy.ready!())
      // Open, with the adoption still in flight.
      expect(userEventsSpy.ready!()).toBe(true)
      // ...and the recorder is HOLDING rather than guessing: it has no base yet,
      // so it must not have decided the chunks are a valid incremental one.
      expect(recorderSpy.instances.at(-1)!.needsFullRewrite).toBe(true)

      resolveAdopt(1)
      await settleUntil(() => !recorderSpy.instances.at(-1)!.needsFullRewrite)
      expect(recorderSpy.instances.at(-1)!.needsFullRewrite).toBe(false)
    }
    finally {
      dispose()
    }
  })

  // The write-side half of the corruption policy: an incremental rewrite is
  // only sound over chunks that are actually on disk.
  it('gives the recorder NO base when the adoption write did not land', async () => {
    await seedSibling('user-1', 'c-other-tab')
    vi.mocked(adoptCheckpoint).mockResolvedValueOnce(null)
    const { runtime, dispose } = mountRuntime(() => 'user-1')
    try {
      await settleUntil(() => userEventsSpy.ready!())
      // Still hydrated -- the state is correct in memory, so the tab resumes.
      expect(runtime.crdtState()!.workspaces['ws-1']).toBeDefined()
      // ...but its persistence restarts from a FULL rewrite.
      expect(recorderSpy.instances.at(-1)!.needsFullRewrite).toBe(true)
      // `needsFullRewrite` alone cannot say WHICH: it is `base !== 'ready'`, so
      // it is equally true while the recorder is still HOLDING -- the sibling
      // test above asserts exactly that. So pin the discriminating half: the
      // recorder must be out of the hold and writing. A regression that never
      // routed the failed adoption into adoptBase would leave it pending
      // forever -- every append held, nothing persisted for the session -- and
      // the assertion above would still pass.
      const recorder = recorderSpy.instances.at(-1)!
      recorder.rewriteNow()
      await settleUntilAsync(async () =>
        (await readCheckpoint('user-1', recorderSpy.options.at(-1)!.clientId)).status === 'ok')
      const own = await readCheckpoint('user-1', recorderSpy.options.at(-1)!.clientId)
      expect(own.status).toBe('ok')
    }
    finally {
      dispose()
    }
  })

  it('cold-starts when the only sibling is past the retention window', async () => {
    await seedSibling('user-1', 'c-other-tab', Date.now() - SEED_CANDIDATE_MAX_AGE_MS - 1)
    const { runtime, dispose } = mountRuntime(() => 'user-1')
    try {
      await settleUntil(() => userEventsSpy.ready!())
      expect(userEventsSpy.ready!()).toBe(true)
      // Cold-started: an EMPTY manager, not the sibling's state. (crdtState is
      // non-null on this path -- the manager exists, it just has nothing in it.)
      expect(runtime.crdtState()!.workspaces['ws-1']).toBeUndefined()
      expect(recorderSpy.instances.at(-1)!.needsFullRewrite).toBe(true)
    }
    finally {
      dispose()
    }
  })

  // The sweep runs AFTER the adoption, and must not collect the row this tab
  // just wrote: isLive short-circuits on keepClientId, and `reserved` excludes
  // it before adding the 1, so it is not double-counted against the cap.
  it('keeps the freshly seeded row when the post-install sweep runs', async () => {
    for (let i = 0; i < CHECKPOINT_MAX_OWNERS + 1; i++)
      await seedSibling('user-1', `c-stale-${i}`, Date.now() - CHECKPOINT_TTL_MS - 1)
    // One fresh sibling to actually seed from.
    await seedSibling('user-1', 'c-other-tab')

    const { dispose } = mountRuntime(() => 'user-1')
    try {
      await settleUntil(() => userEventsSpy.ready!())
      await settleUntilAsync(async () => (await readCheckpoint('user-1', CLIENT)).status === 'ok')
      expect((await readCheckpoint('user-1', CLIENT)).status).toBe('ok')
    }
    finally {
      dispose()
    }
  })
})

// The runtime is the only consumer of onFatalClose, and until now it discarded
// the argument -- every terminal close produced "reload the page", which is the
// one thing a user refused by the connection cap must not do.
describe('useCrdtRuntime fatal close', () => {
  beforeEach(() => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    vi.stubGlobal('IDBKeyRange', IDBKeyRange)
    sessionStorageSet(KEY_CLIENT_ID, CLIENT)
    _resetCheckpointStoreForTest()
    opLog.reset()
    userEventsSpy.ready = null
    userEventsSpy.onFatalClose = null
    toastSpy.sticky = []
    toastSpy.warn = []
  })

  afterEach(() => {
    _resetCheckpointStoreForTest()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('tells a capped user to close a tab, and says it stickily', () => {
    const { dispose } = mountRuntime(() => 'user-a')
    expect(userEventsSpy.onFatalClose).not.toBeNull()

    userEventsSpy.onFatalClose!({ code: 1008, reason: CLOSE_REASON_TOO_MANY_CONNECTIONS })

    expect(toastSpy.sticky).toHaveLength(1)
    expect(toastSpy.sticky[0]).toContain('Close another tab')
    // Sticky, not the 3-second default: nothing retries after a terminal close,
    // so a message that expired would leave a frozen UI unexplained.
    expect(toastSpy.warn).toHaveLength(0)
    dispose()
  })

  it('keeps the reload advice for a revoked credential', () => {
    const { dispose } = mountRuntime(() => 'user-a')

    userEventsSpy.onFatalClose!({ code: 1008, reason: 'credential' })

    expect(toastSpy.sticky).toHaveLength(1)
    expect(toastSpy.sticky[0]).toContain('Reload the page to reconnect')
    dispose()
  })

  // A tab holds TWO long-lived sockets and the cap closes whichever asks next,
  // so one refusal can arrive twice. Two identical sticky toasts cannot be
  // dismissed by the same click and read as two separate faults.
  // The hook must NOT dedupe. Suppression that lives here never learns the user
  // dismissed the toast, so a second refusal after a dismissal would be
  // announced nowhere at all -- a frozen UI with no explanation, the exact state
  // the sticky toast exists to prevent. Collapsing two toasts that are LIVE at
  // once is the toast layer's job, and is pinned in Toast.test.ts.
  it('announces every refusal, leaving duplicate suppression to the toast', () => {
    const { dispose } = mountRuntime(() => 'user-a')

    userEventsSpy.onFatalClose!({ code: 1008, reason: CLOSE_REASON_TOO_MANY_CONNECTIONS })
    userEventsSpy.onFatalClose!({ code: 1008, reason: CLOSE_REASON_TOO_MANY_CONNECTIONS })

    expect(toastSpy.sticky).toHaveLength(2)
    dispose()
  })

  // The channel relay is refused by the SAME hub code path as the user-events
  // stream. It used to discard the close entirely, so a capped user opening a
  // terminal got "Failed to open terminal" and no idea what to do.
  it('surfaces a channel-relay refusal with the same copy', () => {
    const subscribe = vi.spyOn(channelManager, 'onFatalClose')
    const { dispose } = mountRuntime(() => 'user-a')

    expect(subscribe).toHaveBeenCalled()
    subscribe.mock.calls[0]![0]({ code: 1008, reason: CLOSE_REASON_TOO_MANY_CONNECTIONS })

    expect(toastSpy.sticky).toHaveLength(1)
    expect(toastSpy.sticky[0]).toContain('Close another tab')
    dispose()
  })
})

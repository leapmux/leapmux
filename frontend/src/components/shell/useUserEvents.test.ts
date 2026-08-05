import type { UserEventsHook } from './useUserEvents'
import type { WatchUserEvent } from '~/generated/leapmux/v1/user_ops_pb'
import { create, toBinary } from '@bufbuild/protobuf'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  HLCSchema,
  LWWStringSchema,
  TabRecordSchema,
  UserMaterializedSchema,
} from '~/generated/leapmux/v1/user_crdt_pb'
import {
  CrdtOpSchema,
  EntityMaterializedSchema,
  EntityRemovedSchema,
  OpBatchSchema,
  PresenceUpdateSchema,
  SetTabRegisterOpSchema,
  TabIdentSchema,
  WatchUserEventSchema,
  WorkspaceCreatedSchema,
} from '~/generated/leapmux/v1/user_ops_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { uint8ArrayToBase64 } from '~/lib/base64'
import { KEY_USER_EVENTS_RELAY_SEQ, localStorageGet, localStorageSet } from '~/lib/browserStorage'
import { isStatePopulated } from '~/lib/crdt/pendingOps'
import { createActiveClientStore } from '~/lib/presence/activeClient'
import { nextUserEventsRelayId, useUserEvents } from './useUserEvents'

// Controllable stand-in for the Tauri sidecar bridge so a test can drive the
// desktop relay path (isTauriApp() true, no buildWsUrl override) and assert the
// listener/relay teardown. Defaults keep the native-WebSocket tests below (which
// pass buildWsUrl) on their own path -- isTauri stays false and the bridge
// functions are never touched by them.
const bridge = vi.hoisted(() => ({
  isTauri: false,
  handlers: new Map<string, (payload: unknown) => void>(),
  // Every onEvent registration, in order, INCLUDING ones a later attempt has
  // superseded: `handlers` only keeps the latest per name, but the stale-handler
  // tests need to fire a superseded attempt's listener the way Rust would.
  registrations: [] as Array<{ name: string, handler: (payload: unknown) => void }>,
  unlistenCalls: new Map<string, number>(),
  openCalls: 0,
  closeCalls: 0,
  openedRelayIds: [] as number[],
  // Resume token handed to each relay open, so the DESKTOP branch's cursor
  // threading is assertable. The fake used to accept only `relayId`, silently
  // discarding the token -- dropping it in the hook would have shipped green.
  openedResumeTokens: [] as unknown[],
  closedRelayIds: [] as number[],
  // When true, onEvent registers the listener synchronously (as Rust does) but
  // leaves its promise pending until releaseOnEvent() -- the real registration gap,
  // in which bridgeCleanup has nothing to unsubscribe yet.
  deferOnEvent: false,
  pendingOnEvent: [] as Array<() => void>,
  releaseOnEvent(): void {
    const waiting = bridge.pendingOnEvent
    bridge.pendingOnEvent = []
    for (const resolve of waiting) resolve()
  },
}))

vi.mock('~/api/platformBridge', () => ({
  parseRelayClosePayload: (payload: unknown) => {
    const close = payload as { code?: unknown, reason?: unknown, wasClean?: unknown } | null
    return {
      code: typeof close?.code === 'number' ? close.code : 1006,
      reason: typeof close?.reason === 'string' ? close.reason : '',
      wasClean: close?.wasClean === true,
    }
  },
  isTauriApp: () => bridge.isTauri,
  platformBridge: {
    onEvent: (name: string, handler: (payload: unknown) => void) => {
      bridge.handlers.set(name, handler)
      bridge.registrations.push({ name, handler })
      const unlisten = () => bridge.unlistenCalls.set(name, (bridge.unlistenCalls.get(name) ?? 0) + 1)
      if (!bridge.deferOnEvent)
        return Promise.resolve(unlisten)
      return new Promise<() => void>((resolve) => {
        bridge.pendingOnEvent.push(() => resolve(unlisten))
      })
    },
    openUserEventsRelay: async (relayId: number, _workspaceIds?: string[], resume?: unknown) => {
      bridge.openCalls++
      bridge.openedRelayIds.push(relayId)
      bridge.openedResumeTokens.push(resume)
    },
    closeUserEventsRelay: async (relayId: number) => {
      bridge.closeCalls++
      bridge.closedRelayIds.push(relayId)
    },
  },
}))

/**
 * Captured argument bundle for each PendingOpsManager method the hook
 * forwards into. We're not exercising the manager's internal merge
 * logic here — the manager has its own dedicated test suite. The job
 * is to confirm `useUserEvents` routes each `WatchUserEvent` case to
 * the right method exactly once, in the right argument shape.
 */
interface FakePending {
  bootstrap: ReturnType<typeof vi.fn>
  consumeRemote: ReturnType<typeof vi.fn>
  consumeEntityMaterialized: ReturnType<typeof vi.fn>
  consumeEntityRemoved: ReturnType<typeof vi.fn>
  applyDelta: ReturnType<typeof vi.fn>
  consumeBatchEnd: ReturnType<typeof vi.fn>
  clock: { observe: ReturnType<typeof vi.fn> }
  isConfirmedPopulated: () => boolean
  // The refresh-guard reads confirmedState.workspaces/nodes/tabs/floatingWindows
  // via isConfirmedPopulated() to decide whether a resume cursor is safe to
  // send. Populated = an in-session reconnect (state survives in memory);
  // empty/absent = a cold start.
  state: {
    confirmedState: { workspaces: Record<string, unknown>, nodes: Record<string, unknown>, tabs: Record<string, unknown>, floatingWindows: Record<string, unknown> }
    resumeWatermark?: { physical: bigint, logical: bigint, clientId: string }
    currentEpoch: bigint
  }
}

function makeFakePending(opts?: {
  droppedPending?: boolean
  populated?: boolean
  populateMap?: 'workspaces' | 'nodes' | 'tabs' | 'floatingWindows'
  /** The in-memory resume cursor. This IS the only cursor source now. */
  watermark?: { physical: bigint, logical: bigint, clientId: string }
  epoch?: bigint
}): FakePending {
  // `populated` simulates an in-session reconnect: confirmedState already
  // holds an entity, so the refresh-guard sends the resume cursor. `populateMap`
  // targets a specific map (defaults to workspaces) so the OR over all four
  // maps (now centralized in isConfirmedPopulated) can be pinned per-map.
  const map = opts?.populateMap ?? 'workspaces'
  const entry = opts?.populated ? { x1: {} } : {}
  const confirmedState = {
    workspaces: map === 'workspaces' ? entry : {},
    nodes: map === 'nodes' ? entry : {},
    tabs: map === 'tabs' ? entry : {},
    floatingWindows: map === 'floatingWindows' ? entry : {},
  }
  return {
    bootstrap: vi.fn(),
    consumeRemote: vi.fn(),
    consumeEntityMaterialized: vi.fn(),
    consumeEntityRemoved: vi.fn(() => ({ droppedPending: opts?.droppedPending ?? false })),
    applyDelta: vi.fn(() => ({ droppedPending: opts?.droppedPending ?? false })),
    consumeBatchEnd: vi.fn(),
    clock: { observe: vi.fn() },
    // Delegates to the PRODUCTION predicate rather than re-implementing it.
    // A hand-copied mirror meant every cursor-suppression case below asserted
    // against the fake's own branch: the real rule could gain a fifth map or
    // invert a condition and this suite would stay green while the refresh
    // guard it exists to pin was broken.
    isConfirmedPopulated: () => isStatePopulated(confirmedState),
    state: {
      confirmedState,
      // Carried on the fake because production always carries them: every path
      // that populates confirmedState (bootstrap, hydrate, an applied frame)
      // also seeds the watermark and epoch. A fake that omitted them described a
      // state the app never reaches, and let a cursor-source test pass against a
      // shape that could not occur.
      resumeWatermark: opts?.watermark,
      currentEpoch: opts?.epoch ?? 0n,
    },
  }
}

/**
 * fakeSocket simulates the WebSocket the hook would open against
 * `/ws/userevents`. The hook only ever consumes `message`/`close`/
 * `error` events from the listener side, so we expose `emit(event,
 * data)` for the test to push events through synchronously.
 */
class FakeSocket {
  static instances: FakeSocket[] = []
  url: string
  binaryType = 'arraybuffer'
  private listeners = new Map<string, Array<(ev: MessageEvent | Event) => void>>()
  closed = false

  constructor(url: string, _protocols: string[] | string) {
    this.url = url
    FakeSocket.instances.push(this)
  }

  addEventListener(name: string, fn: (ev: MessageEvent | Event) => void): void {
    if (!this.listeners.has(name))
      this.listeners.set(name, [])
    this.listeners.get(name)!.push(fn)
  }

  close(): void {
    this.closed = true
    this.emit('close', new Event('close'))
  }

  emit(name: string, ev: MessageEvent | Event): void {
    const fns = this.listeners.get(name) ?? []
    for (const fn of fns) fn(ev)
  }

  /** Frame a WatchUserEvent the way the hub does (length-prefixed proto). */
  sendEvent(evt: WatchUserEvent): void {
    const payload = toBinary(WatchUserEventSchema, evt)
    const buf = new ArrayBuffer(4 + payload.length)
    const view = new DataView(buf)
    view.setUint32(0, payload.length, false) // big-endian
    new Uint8Array(buf, 4).set(payload)
    this.emit('message', { data: buf } as MessageEvent)
  }
}

beforeEach(() => {
  // Clear persisted state so the relay-id allocator tests below start from a
  // fresh seed (mark = null), independent of whatever the bridge-path tests
  // above persisted to KEY_USER_EVENTS_RELAY_SEQ. Without this, the first test
  // in the nextusereventsrelayid block reads a non-null mark left by prior
  // tests and its assertions become order-dependent.
  localStorage.clear()
  bridge.isTauri = false
  bridge.handlers.clear()
  bridge.registrations.length = 0
  bridge.unlistenCalls.clear()
  bridge.openCalls = 0
  bridge.closeCalls = 0
  bridge.openedRelayIds.length = 0
  bridge.openedResumeTokens.length = 0
  bridge.closedRelayIds.length = 0
  bridge.deferOnEvent = false
  bridge.pendingOnEvent.length = 0
  FakeSocket.instances = []
  // The hook constructs the socket via `new WebSocket(url, subprotocols)`;
  // a class with the same constructor + addEventListener / close shape
  // is sufficient. The hook never actually reads from .readyState.
  vi.stubGlobal('WebSocket', FakeSocket as unknown as typeof WebSocket)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

// flushEffects yields a microtask so Solid flushes queued createEffect
// invocations. The useUserEvents hook's WebSocket open happens inside a
// createEffect, so tests must flush after seeding the userId signal
// before the FakeSocket instance is observable.
async function flushEffects(): Promise<void> {
  await Promise.resolve()
}

describe('useUserEvents (websocket dispatch)', () => {
  it('opens a single socket when userId becomes non-empty', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const pending = makeFakePending()
      useUserEvents({
        userId,
        allowedWorkspaceIds: () => ['w1', 'w2'],
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: ws => `ws://test/userevents?w=${ws.join(',')}`,
      })
      await flushEffects()
      expect(FakeSocket.instances).toHaveLength(1)
      // The builder receives the allowed workspace ids and nothing else -- the
      // authenticated session implies the user, so there is no user id to pass.
      expect(FakeSocket.instances[0]!.url).toBe('ws://test/userevents?w=w1,w2')
      dispose()
    })
  })

  it('does not open a socket while userId is empty', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('')
      const pending = makeFakePending()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: ws => `ws://test/userevents?w=${ws.join(',')}`,
      })
      await flushEffects()
      expect(FakeSocket.instances).toHaveLength(0)
      dispose()
    })
  })

  it('does not open a socket until the `ready` hydration gate flips true', async () => {
    // useCrdtRuntime passes `ready: hydrated` so the WS open effect waits for
    // the persisted checkpoint + op-log to be loaded + replayed before opening.
    // The resume cursor must resolve against hydrated confirmedState (the tight
    // T_now), not empty state — so the socket must not open while ready() is
    // false, then open once it flips true (re-firing the effect via the dep).
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const [ready, setReady] = createSignal(false)
      const pending = makeFakePending({ populated: true })
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        ready,
        buildWsUrl: ws => `ws://test/userevents?w=${ws.join(',')}`,
      })
      await flushEffects()
      // Hydration not complete: no socket yet even though userId is set.
      expect(FakeSocket.instances).toHaveLength(0)
      setReady(true)
      await flushEffects()
      expect(FakeSocket.instances).toHaveLength(1)
      dispose()
    })
  })

  it('opens immediately when no `ready` gate is supplied (prior behavior)', async () => {
    // Callers that don't wire hydration get readyGate = () => true, so the
    // socket opens as soon as userId is truthy — the pre-hydration behavior.
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const pending = makeFakePending()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: ws => `ws://test/userevents?w=${ws.join(',')}`,
      })
      await flushEffects()
      expect(FakeSocket.instances).toHaveLength(1)
      dispose()
    })
  })

  it('threads the resume watermark into the URL builder', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      // populated = an in-session reconnect: confirmedState holds a workspace,
      // so the refresh-guard sends the cursor.
      const pending = makeFakePending({
        populated: true,
        watermark: { physical: 1754100000000n, logical: 3n, clientId: 'c-abc' },
        epoch: 7n,
      })
      let captured: { hlc: { physical: bigint, logical: bigint, clientId: string }, epoch: bigint } | null | undefined
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: (ws, resume) => {
          captured = resume
          return `ws://test/userevents?w=${ws.join(',')}`
        },
      })
      await flushEffects()
      expect(captured).toBeDefined()
      expect(captured!.hlc.physical).toBe(1754100000000n)
      expect(captured!.hlc.clientId).toBe('c-abc')
      expect(captured!.epoch).toBe(7n)
      dispose()
    })
  })

  // A NARROWED subscription must present no cursor. The persisted cursor is
  // per-user, and since the checkpoint seed landed it is cross-tab too, so
  // replaying one minted under a narrow filter against a wider one can miss ops
  // -- the hazard SubscribeWithACL documents. Enforcing it here makes the bad
  // pairing unspellable instead of a comment a future producer of
  // `allowedWorkspaceIds` has to notice.
  it('suppresses the resume cursor when the subscription is narrowed by workspace_ids', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-narrowed')
      // Everything the cursor needs is present: populated confirmedState AND a
      // valid watermark. The ONLY reason it must be withheld is the narrowing.
      const pending = makeFakePending({
        populated: true,
        watermark: { physical: 1754100000000n, logical: 0n, clientId: 'c-w' },
        epoch: 3n,
      })
      let captured: unknown = 'unset'
      let capturedWorkspaceIds: string[] = []
      useUserEvents({
        userId,
        allowedWorkspaceIds: () => ['w1', 'w2'],
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: (ws, resume) => {
          captured = resume
          capturedWorkspaceIds = [...ws]
          return `ws://test/userevents?w=${ws.join(',')}`
        },
      })
      await flushEffects()
      expect(captured).toBeNull()
      // ...and the narrowing itself still reaches the URL. The suppression
      // reads the SAME accessor value the builder is handed (one read per
      // connect, not three), so a refactor that decoupled them could silently
      // drop the filter while this test still saw a null cursor.
      expect(capturedWorkspaceIds).toEqual(['w1', 'w2'])
      dispose()
    })
  })

  it('suppresses the resume cursor when confirmedState is empty even though a watermark exists', async () => {
    // Hydration that found no checkpoint leaves confirmedState empty. Sending a
    // cursor then would make the hub ship a delta the client folds onto empty
    // maps (a partial result), so the guard suppresses it and takes a full
    // snapshot instead.
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-cold')
      const pending = makeFakePending({
        watermark: { physical: 999n, logical: 0n, clientId: 'c-stale' },
        epoch: 3n,
      }) // empty confirmedState (cold start)
      let captured: unknown = 'unset'
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: (_ws, resume) => {
          captured = resume
          return 'ws://test/userevents'
        },
      })
      await flushEffects()
      expect(captured).toBeNull()
      dispose()
    })
  })

  it('sends the resume cursor when confirmedState has only nodes (not workspaces)', async () => {
    // The refresh-guard treats ANY of workspaces/nodes/tabs as "populated".
    // Pin the OR per-map so a future change that narrowed the check to just
    // workspaces (and regressed the transient nodes-but-no-workspace-record
    // case) would fail loudly.
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-nodes-only')
      const pending = makeFakePending({ populated: true, populateMap: 'nodes', watermark: { physical: 500n, logical: 1n, clientId: 'c-n' }, epoch: 2n })
      let captured: unknown = 'unset'
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: (_ws, resume) => {
          captured = resume
          return 'ws://test/userevents'
        },
      })
      await flushEffects()
      expect(captured).not.toBeNull()
      dispose()
    })
  })

  it('sends the resume cursor when confirmedState has only floating windows', async () => {
    // The refresh-guard enumerates EVERY top-level map of confirmedState,
    // including floatingWindows (a user whose only state is a detached floating
    // window must still count as populated). Pin it so a future map addition
    // or a narrowing to just workspaces/nodes/tabs fails loudly.
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-fw-only')
      const pending = makeFakePending({ populated: true, populateMap: 'floatingWindows', watermark: { physical: 600n, logical: 0n, clientId: 'c-fw' }, epoch: 4n })
      let captured: unknown = 'unset'
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: (_ws, resume) => {
          captured = resume
          return 'ws://test/userevents'
        },
      })
      await flushEffects()
      expect(captured).not.toBeNull()
      dispose()
    })
  })

  it('suppresses the resume cursor when pending() returns null (no manager yet)', async () => {
    // The manager can be null briefly before the userId effect constructs it.
    // A watermark present during that window must not crash the guard (it
    // reads pending()?.state) and must yield a null cursor.
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-null-pending')
      let captured: unknown = 'unset'
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => null as never,
        buildWsUrl: (_ws, resume) => {
          captured = resume
          return 'ws://test/userevents'
        },
      })
      await flushEffects()
      expect(captured).toBeNull()
      dispose()
    })
  })

  it('passes a null resume token when there is no watermark', async () => {
    // No watermark on the manager: nothing to resume from.
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-no-wm')
      const pending = makeFakePending()
      let captured: unknown = 'unset'
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: (_ws, resume) => {
          captured = resume
          return 'ws://test/userevents'
        },
      })
      await flushEffects()
      expect(captured).toBeNull()
      dispose()
    })
  })

  it('tears down the prior socket and opens a fresh one on userId change', async () => {
    await createRoot(async (dispose) => {
      const [userId, setUserId] = createSignal('user-1')
      const pending = makeFakePending()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: ws => `ws://test/userevents?w=${ws.join(',')}`,
      })
      await flushEffects()
      const first = FakeSocket.instances[0]!
      expect(first.closed).toBe(false)

      setUserId('user-2')
      await flushEffects()
      expect(first.closed).toBe(true)
      expect(FakeSocket.instances).toHaveLength(2)
      expect(FakeSocket.instances[1]!.closed).toBe(false)
      dispose()
    })
  })

  it('routes the initial UserMaterialized frame into pending.bootstrap and sets bootstrapped', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const pending = makeFakePending()
      const hook = useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: () => 'ws://test/userevents',
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!
      expect(hook.bootstrapped()).toBe(false)

      const initial = create(UserMaterializedSchema, {
        userId: 'user-1',
        nodes: {},
        tabs: {},
        floatingWindows: {},
        workspaces: {},
        currentEpoch: 7n,
      })
      sock.sendEvent(create(WatchUserEventSchema, {
        event: { case: 'initial', value: initial },
      }))

      expect(pending.bootstrap).toHaveBeenCalledTimes(1)
      const arg = pending.bootstrap.mock.calls[0]![0] as { currentEpoch: bigint }
      expect(arg.currentEpoch).toBe(7n)
      expect(hook.bootstrapped()).toBe(true)
      dispose()
    })
  })

  // The client half of crdt.Manager.requireOwnState. UserMaterialized carries
  // its own user_id, so adopting it unconditionally would let the FRAME -- not
  // the socket it arrived on -- decide whose workspaces, tiles and tabs the
  // shell renders. The per-socket generation guards answer "is this frame
  // stale?", never "is this frame mine?", so nothing else here would notice.
  it('refuses a materialized payload naming another tenant', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const pending = makeFakePending()
      const hook = useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: () => 'ws://test/userevents',
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      sock.sendEvent(create(WatchUserEventSchema, {
        event: {
          case: 'initial',
          value: create(UserMaterializedSchema, { userId: 'someone-else', currentEpoch: 9n }),
        },
      }))

      expect(pending.bootstrap).not.toHaveBeenCalled()
      expect(hook.bootstrapped()).toBe(false)

      // Control: the same frame for the right tenant IS adopted, so the
      // assertion above means "refused" rather than "this fixture never
      // bootstraps at all".
      sock.sendEvent(create(WatchUserEventSchema, {
        event: {
          case: 'initial',
          value: create(UserMaterializedSchema, { userId: 'user-1', currentEpoch: 9n }),
        },
      }))
      expect(pending.bootstrap).toHaveBeenCalledTimes(1)
      expect(hook.bootstrapped()).toBe(true)
      dispose()
    })
  })

  // Strict framing, mirroring channel.ts: a frame with trailing bytes after the
  // declared payload length is a protocol violation dropped whole. Quietly
  // decoding the valid prefix would mask a hub<->frontend framing desync until
  // some later change starts depending on it.
  it('drops a frame with trailing bytes after the declared payload', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const pending = makeFakePending()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: () => 'ws://test/userevents',
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      const initial = create(UserMaterializedSchema, { userId: 'user-1', currentEpoch: 1n })
      const payload = toBinary(WatchUserEventSchema, create(WatchUserEventSchema, {
        event: { case: 'initial', value: initial },
      }))
      const buf = new Uint8Array(4 + payload.length + 3) // 3 trailing garbage bytes
      new DataView(buf.buffer).setUint32(0, payload.length, false)
      buf.set(payload, 4)
      sock.emit('message', { data: buf } as MessageEvent)

      expect(pending.bootstrap).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('routes a batch frame into pending.consumeRemote', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const pending = makeFakePending()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: () => 'ws://test/userevents',
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      const op = create(CrdtOpSchema, {
        opId: 'op-abc',
        body: {
          case: 'setTabRegister',
          value: create(SetTabRegisterOpSchema, {
            tabType: TabType.AGENT,
            tabId: 'tA',
            field: { case: 'tileId', value: 'root1' },
          }),
        },
      })
      const batch = create(OpBatchSchema, { batchId: 'b-1', ops: [op] })
      sock.sendEvent(create(WatchUserEventSchema, { event: { case: 'batch', value: batch } }))

      expect(pending.consumeRemote).toHaveBeenCalledTimes(1)
      expect((pending.consumeRemote.mock.calls[0]![0] as { batchId: string }).batchId).toBe('b-1')
      dispose()
    })
  })

  it('routes EntityMaterialized into pending.consumeEntityMaterialized', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const pending = makeFakePending()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: () => 'ws://test/userevents',
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      const tab = create(TabRecordSchema, {
        tabType: TabType.AGENT,
        tabId: 'tA',
        tileId: create(LWWStringSchema, {
          value: 'root1',
          hlc: create(HLCSchema, { physical: 1n, logical: 0n, clientId: 'c1' }),
        }),
      })
      const mat = create(EntityMaterializedSchema, { entity: { case: 'tab', value: tab } })
      sock.sendEvent(create(WatchUserEventSchema, { event: { case: 'entityMaterialized', value: mat } }))

      expect(pending.consumeEntityMaterialized).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('routes EntityRemoved into pending.consumeEntityRemoved and reports dropped pending', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const pending = makeFakePending({ droppedPending: true })
      const onPendingDropped = vi.fn()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        onPendingDropped,
        buildWsUrl: () => 'ws://test/userevents',
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      const rem = create(EntityRemovedSchema, {
        entity: {
          case: 'tab',
          value: create(TabIdentSchema, { tabType: TabType.AGENT, tabId: 'tA' }),
        },
      })
      sock.sendEvent(create(WatchUserEventSchema, { event: { case: 'entityRemoved', value: rem } }))

      expect(pending.consumeEntityRemoved).toHaveBeenCalledTimes(1)
      expect(onPendingDropped).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('routes presence into activeClient.update', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const ac = createActiveClientStore()
      useUserEvents({
        userId,
        activeClient: ac,
        pending: () => makeFakePending() as never,
        buildWsUrl: () => 'ws://test/userevents',
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      const pres = create(PresenceUpdateSchema, { workspaceId: 'w1', activeClientId: 'client-a' })
      sock.sendEvent(create(WatchUserEventSchema, { event: { case: 'presence', value: pres } }))

      expect(ac.activeFor('w1')).toBe('client-a')
      dispose()
    })
  })

  it('invokes onWorkspaceLifecycleChanged for created / renamed / deleted', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const onWorkspaceLifecycleChanged = vi.fn()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => makeFakePending() as never,
        onWorkspaceLifecycleChanged,
        buildWsUrl: () => 'ws://test/userevents',
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      const created = create(WorkspaceCreatedSchema, {
        workspaceId: 'w1',
        title: 'My WS',
        rootNodeId: 'r1',
      })
      sock.sendEvent(create(WatchUserEventSchema, { event: { case: 'created', value: created } }))

      expect(onWorkspaceLifecycleChanged).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  // The `delta` arm is now the normal COLD-START path (a refreshed page hydrates
  // from IndexedDB and resumes rather than bootstrapping), so everything the
  // `initial` arm establishes has to be established here too. Two things were
  // missing and are pinned below.
  it('adopts the subscriber client id and refreshes the workspace list on a delta', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const pending = makeFakePending({ populated: true })
      const onSubscriberClientId = vi.fn()
      const onWorkspaceLifecycleChanged = vi.fn()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        onSubscriberClientId,
        onWorkspaceLifecycleChanged,
        buildWsUrl: () => 'ws://test/userevents',
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      sock.sendEvent(create(WatchUserEventSchema, {
        event: {
          case: 'delta',
          value: {
            frames: [],
            maxHlc: create(HLCSchema, { physical: 300n, logical: 0n, clientId: 'hub' }),
            currentEpoch: 2n,
            subscriberClientId: 'client-from-hub',
          },
        },
      }))

      expect(pending.applyDelta).toHaveBeenCalledTimes(1)
      // Without this the active-client gate compares against '' for the life of
      // the page, which disables it outright -- every refreshed tab would play
      // the turn-end sound regardless of which client is active.
      expect(onSubscriberClientId).toHaveBeenCalledWith('client-from-hub')
      // Workspace lifecycle frames are not replayed in a delta, and the sidebar
      // list comes from a separate listWorkspaces driven by this callback -- so
      // without it a create/rename/delete during the gap never reaches the UI.
      expect(onWorkspaceLifecycleChanged).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  // The third thing the `initial` arm establishes that the `delta` arm did not:
  // a tenancy check. `initial` refuses a foreign UserMaterialized, and the
  // cold-start hydration path refuses a foreign checkpoint; the delta arm --
  // now the normal cold start -- failed OPEN, and could not have done otherwise
  // because ResumeDelta carried no user_id to compare. Adopting one would fold
  // another tenant's records into confirmedState AND, via the op-log observer,
  // into this device's IndexedDB checkpoint, where they survive refreshes.
  it('refuses a delta that names a different tenant', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const pending = makeFakePending({ populated: true })
      const onSubscriberClientId = vi.fn()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        onSubscriberClientId,
        buildWsUrl: () => 'ws://test/userevents',
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      sock.sendEvent(create(WatchUserEventSchema, {
        event: {
          case: 'delta',
          value: {
            frames: [],
            maxHlc: create(HLCSchema, { physical: 300n, logical: 0n, clientId: 'hub' }),
            currentEpoch: 2n,
            subscriberClientId: 'client-from-hub',
            userId: 'someone-else',
          },
        },
      }))

      expect(pending.applyDelta).not.toHaveBeenCalled()
      // Nothing downstream of the refusal may run either -- adopting the hub's
      // subscriber identity from a frame we just rejected would be incoherent.
      expect(onSubscriberClientId).not.toHaveBeenCalled()
      dispose()
    })
  })

  // `initial` and `delta` are the two bootstrap-bearing arms, and they adopt
  // the SAME four things in the SAME order. They used to hand-copy that tail
  // and had drifted twice -- the delta arm shipped without the subscriber-id
  // adoption and without the workspace-list refresh. This pins both halves:
  // the sequence each arm runs, and that the two are identical.
  it('runs the same adoption sequence for the initial and the delta arm', async () => {
    interface AdoptionTrace {
      /** Callback names in fire order. */
      order: string[]
      /** `bootstrapped()` / `clock()` sampled from inside each callback. */
      liveAtSubscriber: { bootstrapped: boolean, clock: boolean }
      liveAtLifecycle: { bootstrapped: boolean, clock: boolean }
    }

    const traceFor = (arm: 'initial' | 'delta'): Promise<AdoptionTrace> =>
      createRoot(async (dispose) => {
        const [userId] = createSignal('user-1')
        const pending = makeFakePending({ populated: true })
        const trace: AdoptionTrace = {
          order: [],
          liveAtSubscriber: { bootstrapped: false, clock: false },
          liveAtLifecycle: { bootstrapped: false, clock: false },
        }
        // Assigned before any frame can arrive; the callbacks below only run
        // from a socket message, which the test drives after `useUserEvents`
        // has returned.
        let hook: UserEventsHook | undefined
        const sample = (): { bootstrapped: boolean, clock: boolean } => ({
          bootstrapped: hook?.bootstrapped() ?? false,
          clock: (hook?.clock() ?? null) !== null,
        })
        hook = useUserEvents({
          userId,
          activeClient: createActiveClientStore(),
          pending: () => pending as never,
          onSubscriberClientId: () => {
            trace.order.push('subscriber')
            trace.liveAtSubscriber = sample()
          },
          onWorkspaceLifecycleChanged: () => {
            trace.order.push('lifecycle')
            trace.liveAtLifecycle = sample()
          },
          buildWsUrl: () => 'ws://test/userevents',
        })
        await flushEffects()
        const sock = FakeSocket.instances.at(-1)!

        sock.sendEvent(create(WatchUserEventSchema, arm === 'initial'
          ? {
              event: {
                case: 'initial',
                value: create(UserMaterializedSchema, {
                  userId: 'user-1',
                  subscriberClientId: 'client-from-hub',
                  currentEpoch: 2n,
                  maxHlc: create(HLCSchema, { physical: 300n, logical: 0n, clientId: 'hub' }),
                }),
              },
            }
          : {
              event: {
                case: 'delta',
                value: {
                  frames: [],
                  maxHlc: create(HLCSchema, { physical: 300n, logical: 0n, clientId: 'hub' }),
                  currentEpoch: 2n,
                  subscriberClientId: 'client-from-hub',
                  userId: 'user-1',
                },
              },
            }))

        dispose()
        return trace
      })

    const initial = await traceFor('initial')
    const delta = await traceFor('delta')
    expect(initial).toEqual(delta)
    // ...and it is the RIGHT sequence, not two identical wrong ones. The
    // subscriber id is adopted BEFORE the shell is told the CRDT is live, so
    // nothing downstream of `bootstrapped` can read a '' identity; the
    // workspace refresh fires LAST, once the clock and the flag are both set.
    expect(initial.order).toEqual(['subscriber', 'lifecycle'])
    expect(initial.liveAtSubscriber).toEqual({ bootstrapped: false, clock: false })
    expect(initial.liveAtLifecycle).toEqual({ bootstrapped: true, clock: true })
  })

  it('accepts a delta whose user_id matches', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const pending = makeFakePending({ populated: true })
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: () => 'ws://test/userevents',
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      sock.sendEvent(create(WatchUserEventSchema, {
        event: {
          case: 'delta',
          value: {
            frames: [],
            maxHlc: create(HLCSchema, { physical: 300n, logical: 0n, clientId: 'hub' }),
            currentEpoch: 2n,
            userId: 'user-1',
          },
        },
      }))

      expect(pending.applyDelta).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('routes a batchEnd frame to the manager (the only watermark-advance point)', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const pending = makeFakePending()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: () => 'ws://test/userevents',
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      const atHlc = create(HLCSchema, { physical: 400n, logical: 2n, clientId: 'hub' })
      sock.sendEvent(create(WatchUserEventSchema, { event: { case: 'batchEnd', value: { atHlc } } }))

      expect(pending.consumeBatchEnd).toHaveBeenCalledTimes(1)
      expect(pending.consumeBatchEnd.mock.calls[0][0]).toMatchObject({ physical: 400n, logical: 2n })
      dispose()
    })
  })

  it('ignores a message that arrives on a socket superseded by teardown', async () => {
    await createRoot(async (dispose) => {
      const [userId, setUserId] = createSignal('user-1')
      const pending = makeFakePending()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: () => 'ws://test/userevents',
      })
      await flushEffects()
      const stale = FakeSocket.instances[0]!

      // A userId change tears down the first socket and opens a fresh one.
      setUserId('user-2')
      await flushEffects()
      expect(FakeSocket.instances).toHaveLength(2)

      // A frame still queued on the OLD (superseded) socket must be dropped by the
      // message handler's stale-connection guard -- otherwise it would re-bootstrap
      // the still-live PendingOpsManager to a stale snapshot (resetting currentEpoch
      // and re-arming the epoch loop). The close/error handlers already guard this;
      // the message handler must too.
      const staleInitial = create(UserMaterializedSchema, {
        userId: 'user-1',
        nodes: {},
        tabs: {},
        floatingWindows: {},
        workspaces: {},
        currentEpoch: 99n,
      })
      stale.sendEvent(create(WatchUserEventSchema, { event: { case: 'initial', value: staleInitial } }))

      expect(pending.bootstrap).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('ignores malformed frames silently', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const pending = makeFakePending()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: () => 'ws://test/userevents',
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      // Frame with length prefix but bogus proto payload.
      const buf = new ArrayBuffer(8)
      const view = new DataView(buf)
      view.setUint32(0, 4, false)
      view.setUint32(4, 0xDEADBEEF, false)
      sock.emit('message', { data: buf } as MessageEvent)

      expect(pending.consumeRemote).not.toHaveBeenCalled()
      expect(pending.bootstrap).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('clears bootstrapped on close so a reconnect re-bootstraps', async () => {
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const pending = makeFakePending()
      const hook = useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: () => 'ws://test/userevents',
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      const initial = create(UserMaterializedSchema, {
        userId: 'user-1',
        nodes: {},
        tabs: {},
        floatingWindows: {},
        workspaces: {},
        currentEpoch: 1n,
      })
      sock.sendEvent(create(WatchUserEventSchema, { event: { case: 'initial', value: initial } }))
      expect(hook.bootstrapped()).toBe(true)

      sock.emit('close', new Event('close'))
      expect(hook.bootstrapped()).toBe(false)
      dispose()
    })
  })

  it('reconnects after an unexpected close but not after intentional teardown', async () => {
    vi.useFakeTimers()
    try {
      await createRoot(async (dispose) => {
        const [userId, setUserId] = createSignal('user-1')
        useUserEvents({
          userId,
          activeClient: createActiveClientStore(),
          pending: () => makeFakePending() as never,
          buildWsUrl: () => 'ws://test/userevents',
        })
        await flushEffects()
        FakeSocket.instances[0]!.emit('close', new Event('close'))

        // First reconnect delay is 250ms +/- 20% jitter (< 300ms); advance past
        // the jitter ceiling so the retry fires deterministically.
        await vi.advanceTimersByTimeAsync(300)
        await flushEffects()
        expect(FakeSocket.instances).toHaveLength(2)

        setUserId('user-2')
        await flushEffects()
        expect(FakeSocket.instances).toHaveLength(3)
        await vi.advanceTimersByTimeAsync(5_000)
        expect(FakeSocket.instances).toHaveLength(3)
        dispose()
      })
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('stops reconnecting on a terminal close code and reports it via onFatalClose', async () => {
    vi.useFakeTimers()
    try {
      await createRoot(async (dispose) => {
        const [userId] = createSignal('user-1')
        const onFatalClose = vi.fn()
        useUserEvents({
          userId,
          activeClient: createActiveClientStore(),
          pending: () => makeFakePending() as never,
          buildWsUrl: () => 'ws://test/userevents',
          onFatalClose,
        })
        await flushEffects()
        // 1008 = policy violation (the hub's /ws/userevents "forbidden" / auth
        // expiry): reconnecting would loop, so surface it and stop.
        FakeSocket.instances[0]!.emit('close', { code: 1008, reason: 'forbidden' } as unknown as CloseEvent)
        await vi.advanceTimersByTimeAsync(5_000)
        await flushEffects()
        expect(FakeSocket.instances).toHaveLength(1)
        expect(onFatalClose).toHaveBeenCalledWith({ code: 1008, reason: 'forbidden' })
        dispose()
      })
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('cancels an error-armed reconnect when a terminal close follows on the native path', async () => {
    vi.useFakeTimers()
    try {
      await createRoot(async (dispose) => {
        const [userId] = createSignal('user-1')
        const onFatalClose = vi.fn()
        useUserEvents({
          userId,
          activeClient: createActiveClientStore(),
          pending: () => makeFakePending() as never,
          buildWsUrl: () => 'ws://test/userevents',
          onFatalClose,
        })
        await flushEffects()
        const sock = FakeSocket.instances[0]!
        // A real socket already closing does not synchronously re-fire close from
        // the error handler's ws.close(); model that so the transport error and
        // the server's terminal close arrive as the two distinct events a browser
        // delivers, rather than the FakeSocket default of a synthetic close.
        sock.close = () => {}
        // 1) A transport error arms a reconnect -- the error handler has no way to
        //    know a terminal-coded close is next.
        sock.emit('error', new Event('error'))
        // 2) The server's policy-violation (1008) close then lands. Its tearDown
        //    must cancel the armed retry; without it the timer fires and
        //    resubscribes the connection the fatal close was meant to stop.
        sock.emit('close', { code: 1008, reason: 'forbidden' } as unknown as CloseEvent)

        await vi.advanceTimersByTimeAsync(5_000)
        await flushEffects()
        expect(onFatalClose).toHaveBeenCalledWith({ code: 1008, reason: 'forbidden' })
        expect(FakeSocket.instances).toHaveLength(1)
        dispose()
      })
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('reconnects on an abnormal transport-drop close without firing onFatalClose', async () => {
    vi.useFakeTimers()
    try {
      await createRoot(async (dispose) => {
        const [userId] = createSignal('user-1')
        const onFatalClose = vi.fn()
        useUserEvents({
          userId,
          activeClient: createActiveClientStore(),
          pending: () => makeFakePending() as never,
          buildWsUrl: () => 'ws://test/userevents',
          onFatalClose,
        })
        await flushEffects()
        // 1006 = abnormal closure (a transport drop with no close frame): a
        // network blip must reconnect, not surface as a terminal failure.
        FakeSocket.instances[0]!.emit('close', { code: 1006, reason: '' } as unknown as CloseEvent)
        await vi.advanceTimersByTimeAsync(300)
        await flushEffects()
        expect(FakeSocket.instances).toHaveLength(2)
        expect(onFatalClose).not.toHaveBeenCalled()
        dispose()
      })
    }
    finally {
      vi.useRealTimers()
    }
  })
})

describe('useUserEvents (desktop bridge path)', () => {
  // Flush the effect, the onEvent Promise.all, and the .then that opens the
  // relay -- the desktop path is several microtasks deep before it is live.
  async function settleBridge(): Promise<void> {
    for (let i = 0; i < 6; i++)
      await flushEffects()
  }

  it('opens the relay and registers the bridge listeners', async () => {
    bridge.isTauri = true
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => makeFakePending() as never,
      })
      await settleBridge()
      expect(bridge.openCalls).toBe(1)
      expect(bridge.handlers.has('userevents:message')).toBe(true)
      expect(bridge.handlers.has('userevents:close')).toBe(true)
      dispose()
    })
  })

  // The browser branch's cursor threading is covered via the buildWsUrl
  // override, but the DESKTOP branch hands the token to the sidecar as an RPC
  // argument -- a second serialization hop with its own chance to drop it.
  // Nothing asserted that hop, so removing the third argument at the
  // openUserEventsRelay call site would have shipped green.
  it('threads the resume watermark into the relay open', async () => {
    bridge.isTauri = true
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        // populated => an in-session reconnect, so the cursor is sent.
        pending: () => makeFakePending({
          populated: true,
          watermark: { physical: 1754100000000n, logical: 3n, clientId: 'c-abc' },
          epoch: 7n,
        }) as never,
      })
      await settleBridge()
      expect(bridge.openCalls).toBe(1)
      const resume = bridge.openedResumeTokens[0] as {
        hlc: { physical: bigint, logical: bigint, clientId: string }
        epoch: bigint
      } | null
      expect(resume).toBeTruthy()
      expect(resume!.hlc.physical).toBe(1754100000000n)
      expect(resume!.hlc.clientId).toBe('c-abc')
      expect(resume!.epoch).toBe(7n)
      dispose()
    })
  })

  it('passes a null resume token to the relay open when there is no watermark', async () => {
    bridge.isTauri = true
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => makeFakePending({ populated: true }) as never,
      })
      await settleBridge()
      expect(bridge.openCalls).toBe(1)
      expect(bridge.openedResumeTokens[0] ?? null).toBeNull()
      dispose()
    })
  })

  it('tears down the bridge listeners and closes the relay on a terminal close', async () => {
    bridge.isTauri = true
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const onFatalClose = vi.fn()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => makeFakePending() as never,
        onFatalClose,
      })
      await settleBridge()

      // A terminal close (1008 policy violation / auth expiry) must stop retrying
      // AND release the bridge resources -- the platformBridge listeners and the
      // Go-side relay are not GC-reclaimed like a native WebSocket, so leaving
      // them attached would leak (and double-dispatch on a later re-subscribe).
      bridge.handlers.get('userevents:close')!({ code: 1008, reason: 'forbidden' })
      await flushEffects()

      expect(onFatalClose).toHaveBeenCalledWith({ code: 1008, reason: 'forbidden' })
      expect(bridge.unlistenCalls.get('userevents:message')).toBe(1)
      expect(bridge.unlistenCalls.get('userevents:close')).toBe(1)
      expect(bridge.closeCalls).toBe(1)
      dispose()
    })
  })

  it('does not tear down the bridge on a recoverable close (reconnect path)', async () => {
    bridge.isTauri = true
    await createRoot(async (dispose) => {
      const [userId] = createSignal('user-1')
      const onFatalClose = vi.fn()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => makeFakePending() as never,
        onFatalClose,
      })
      await settleBridge()

      // A recoverable close (1006 transport drop) schedules a reconnect and must
      // NOT fire onFatalClose or immediately tear the bridge down.
      bridge.handlers.get('userevents:close')!({ code: 1006, reason: '' })
      await flushEffects()

      expect(onFatalClose).not.toHaveBeenCalled()
      expect(bridge.unlistenCalls.get('userevents:close') ?? 0).toBe(0)
      dispose()
    })
  })

  // A close delivered to a SUPERSEDED attempt must not tear down the generation that
  // replaced it.
  //
  // The attempt's unlisten callbacks only exist once the onEvent promises resolve, so
  // between Rust registering a listener and that microtask, bridgeCleanup marks the
  // attempt disposed but unsubscribes NOTHING -- a close arriving in that window still
  // reaches the stale handler. Unguarded, it ran tearDown() on the CURRENT generation
  // (closing the fresh user's relay) and fired onFatalClose, surfacing AppShell's "Live
  // updates disconnected. Reload the page to reconnect." on a freshly-reconnected session.
  it('ignores a close for an attempt a later userId change superseded', async () => {
    bridge.isTauri = true
    bridge.deferOnEvent = true
    await createRoot(async (dispose) => {
      const [userId, setUserId] = createSignal('user-1')
      const onFatalClose = vi.fn()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => makeFakePending() as never,
        onFatalClose,
      })
      await settleBridge()
      // The first attempt's listeners are registered in Rust, but its onEvent
      // promises have not resolved: the registration gap.
      const staleClose = bridge.registrations.find(r => r.name === 'userevents:close')!.handler
      expect(bridge.openCalls).toBe(0)

      // Switch users: tearDown() supersedes attempt 1 and attempt 2 takes over.
      setUserId('user-2')
      bridge.releaseOnEvent()
      await settleBridge()
      expect(bridge.openCalls).toBe(1)
      const successorRelayId = bridge.openedRelayIds[0]
      const closesBefore = [...bridge.closedRelayIds]

      // Attempt 1's close lands late, on a listener nothing has unsubscribed yet.
      staleClose({ code: 1008, reason: 'forbidden' })
      await settleBridge()

      expect(onFatalClose).not.toHaveBeenCalled()
      expect(bridge.closedRelayIds).toEqual(closesBefore)
      expect(bridge.closedRelayIds).not.toContain(successorRelayId)
      dispose()
    })
  })

  // Same rule for the message handler: a frame queued for a superseded attempt must
  // not be dispatched into the live PendingOpsManager. A stale `initial` would reset
  // currentEpoch to the snapshot the switch is replacing -- the native path has
  // guarded this since it was written.
  it('ignores a message for an attempt a later userId change superseded', async () => {
    bridge.isTauri = true
    bridge.deferOnEvent = true
    await createRoot(async (dispose) => {
      const [userId, setUserId] = createSignal('user-1')
      const pending = makeFakePending()
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
      })
      await settleBridge()
      const staleMessage = bridge.registrations.find(r => r.name === 'userevents:message')!.handler

      setUserId('user-2')
      bridge.releaseOnEvent()
      await settleBridge()

      const evt = create(WatchUserEventSchema, {
        event: { case: 'initial', value: create(UserMaterializedSchema, { userId: 'user-1', currentEpoch: 3n }) },
      })
      const payload = toBinary(WatchUserEventSchema, evt)
      const framed = new Uint8Array(4 + payload.length)
      new DataView(framed.buffer).setUint32(0, payload.length, false)
      framed.set(payload, 4)
      staleMessage(uint8ArrayToBase64(framed))
      await settleBridge()

      expect(pending.bootstrap).not.toHaveBeenCalled()
      dispose()
    })
  })

  // The relay id the sidecar fences on must pair each attempt's open with its OWN
  // close: the two are separate RPCs run on unordered sidecar goroutines, so a close
  // carrying the successor's id would tear down the relay it names.
  it('closes the relay id it opened, and a fresh id per attempt', async () => {
    bridge.isTauri = true
    await createRoot(async (dispose) => {
      const [userId, setUserId] = createSignal('user-1')
      useUserEvents({
        userId,
        activeClient: createActiveClientStore(),
        pending: () => makeFakePending() as never,
      })
      await settleBridge()
      const firstRelayId = bridge.openedRelayIds[0]

      setUserId('user-2')
      await settleBridge()

      expect(bridge.closedRelayIds).toEqual([firstRelayId])
      const secondRelayId = bridge.openedRelayIds[1]
      expect(secondRelayId).toBeGreaterThan(firstRelayId)

      dispose()
      expect(bridge.closedRelayIds).toEqual([firstRelayId, secondRelayId])
    })
  })
})

// The relay ids must stay ordered across webview reloads: the sidecar outlives
// the reload holding the previous page's owner id, and an open seeded below it
// refuses itself as superseded on every attempt -- user events silently never
// bootstrap. The persisted high-water mark is what carries the ordering through
// a reload. The mark is a plain monotonic counter (NOT the wall clock), so the
// ordering holds regardless of any clock step between page loads.
describe('nextUserEventsRelayId', () => {
  it('hands out strictly increasing ids and persists the high-water mark', () => {
    const first = nextUserEventsRelayId()
    const markAfterFirst = localStorageGet<number>(KEY_USER_EVENTS_RELAY_SEQ)
    const second = nextUserEventsRelayId()
    const markAfterSecond = localStorageGet<number>(KEY_USER_EVENTS_RELAY_SEQ)
    expect(second).toBeGreaterThan(first)
    // The persisted value is the high-water MARK (the id carries it in its high
    // bits plus a per-process random in the low bits), so the mark advances with
    // each allocation and is what a reload reads to continue above the prior
    // page's ids.
    expect(markAfterFirst).toBeGreaterThan(0)
    // markAfterFirst is a number here (the assertion above would have failed
    // otherwise); the non-null assertion satisfies toBeGreaterThan's numeric arg.
    expect(markAfterSecond).toBeGreaterThan(markAfterFirst!)
  })

  it('continues above the persisted mark across a reload', async () => {
    // The previous page left its last id as the persisted mark; the sidecar
    // still holds a relay owned by that id. A reload re-seeds the module --
    // simulated with a fresh module registry -- and must continue above it.
    const staleOwner = 1_000_000
    localStorageSet(KEY_USER_EVENTS_RELAY_SEQ, staleOwner)
    vi.resetModules()
    const fresh = await import('./useUserEvents')
    expect(fresh.nextUserEventsRelayId()).toBeGreaterThan(staleOwner)
  })

  it('honors a small persisted mark rather than seeding from the clock', async () => {
    // The mark is a counter, not the clock: a small persisted mark is honored
    // and the next id advances from it, regardless of the (much larger) wall
    // clock.
    localStorageSet(KEY_USER_EVENTS_RELAY_SEQ, 1234)
    vi.resetModules()
    const fresh = await import('./useUserEvents')
    const id = fresh.nextUserEventsRelayId()
    // The id advances from the persisted mark (1235 * stride), which for any
    // TAB_BITS >= 1 is far below Date.now() (~1.78e12). A clock-seeded scheme
    // would instead produce an id above Date.now().
    expect(id).toBeLessThan(Date.now())
  })
})

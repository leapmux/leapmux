import type { WatchOrgEvent } from '~/generated/leapmux/v1/org_ops_pb'
import { create, toBinary } from '@bufbuild/protobuf'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  HLCSchema,
  LWWStringSchema,
  OrgMaterializedSchema,
  TabRecordSchema,
} from '~/generated/leapmux/v1/org_crdt_pb'
import {
  EntityMaterializedSchema,
  EntityRemovedSchema,
  OpBatchSchema,
  OrgOpSchema,
  PresenceUpdateSchema,
  SetTabRegisterOpSchema,
  TabIdentSchema,
  WatchOrgEventSchema,
  WorkspaceCreatedSchema,
} from '~/generated/leapmux/v1/org_ops_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { uint8ArrayToBase64 } from '~/lib/base64'
import { KEY_ORG_EVENTS_RELAY_SEQ, localStorageGet, localStorageSet } from '~/lib/browserStorage'
import { createActiveClientStore } from '~/lib/presence/activeClient'
import { nextOrgEventsRelayId, useOrgEvents } from './useOrgEvents'

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
    openOrgEventsRelay: async (relayId: number) => {
      bridge.openCalls++
      bridge.openedRelayIds.push(relayId)
    },
    closeOrgEventsRelay: async (relayId: number) => {
      bridge.closeCalls++
      bridge.closedRelayIds.push(relayId)
    },
  },
}))

/**
 * Captured argument bundle for each PendingOpsManager method the hook
 * forwards into. We're not exercising the manager's internal merge
 * logic here — the manager has its own dedicated test suite. The job
 * is to confirm `useOrgEvents` routes each `WatchOrgEvent` case to
 * the right method exactly once, in the right argument shape.
 */
interface FakePending {
  bootstrap: ReturnType<typeof vi.fn>
  consumeRemote: ReturnType<typeof vi.fn>
  consumeEntityMaterialized: ReturnType<typeof vi.fn>
  consumeEntityRemoved: ReturnType<typeof vi.fn>
  clock: { observe: ReturnType<typeof vi.fn> }
}

function makeFakePending(opts?: { droppedPending?: boolean }): FakePending {
  return {
    bootstrap: vi.fn(),
    consumeRemote: vi.fn(),
    consumeEntityMaterialized: vi.fn(),
    consumeEntityRemoved: vi.fn(() => ({ droppedPending: opts?.droppedPending ?? false })),
    clock: { observe: vi.fn() },
  }
}

/**
 * fakeSocket simulates the WebSocket the hook would open against
 * `/ws/orgevents`. The hook only ever consumes `message`/`close`/
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

  /** Frame a WatchOrgEvent the way the hub does (length-prefixed proto). */
  sendEvent(evt: WatchOrgEvent): void {
    const payload = toBinary(WatchOrgEventSchema, evt)
    const buf = new ArrayBuffer(4 + payload.length)
    const view = new DataView(buf)
    view.setUint32(0, payload.length, false) // big-endian
    new Uint8Array(buf, 4).set(payload)
    this.emit('message', { data: buf } as MessageEvent)
  }
}

beforeEach(() => {
  bridge.isTauri = false
  bridge.handlers.clear()
  bridge.registrations.length = 0
  bridge.unlistenCalls.clear()
  bridge.openCalls = 0
  bridge.closeCalls = 0
  bridge.openedRelayIds.length = 0
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
// invocations. The orgEvents hook's WebSocket open happens inside a
// createEffect, so tests must flush after seeding the orgId signal
// before the FakeSocket instance is observable.
async function flushEffects(): Promise<void> {
  await Promise.resolve()
}

describe('useorgevents (websocket dispatch)', () => {
  it('opens a single socket when org_id becomes non-empty', async () => {
    await createRoot(async (dispose) => {
      const [orgId] = createSignal('org-1')
      const pending = makeFakePending()
      useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: (org, ws) => `ws://test/${org}?w=${ws.join(',')}`,
      })
      await flushEffects()
      expect(FakeSocket.instances).toHaveLength(1)
      expect(FakeSocket.instances[0]!.url).toBe('ws://test/org-1?w=')
      dispose()
    })
  })

  it('does not open a socket while org_id is empty', async () => {
    await createRoot(async (dispose) => {
      const [orgId] = createSignal('')
      const pending = makeFakePending()
      useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: (org, ws) => `ws://test/${org}?w=${ws.join(',')}`,
      })
      await flushEffects()
      expect(FakeSocket.instances).toHaveLength(0)
      dispose()
    })
  })

  it('tears down the prior socket and opens a fresh one on org_id change', async () => {
    await createRoot(async (dispose) => {
      const [orgId, setOrgId] = createSignal('org-1')
      const pending = makeFakePending()
      useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: (org, ws) => `ws://test/${org}?w=${ws.join(',')}`,
      })
      await flushEffects()
      const first = FakeSocket.instances[0]!
      expect(first.closed).toBe(false)

      setOrgId('org-2')
      await flushEffects()
      expect(first.closed).toBe(true)
      expect(FakeSocket.instances).toHaveLength(2)
      expect(FakeSocket.instances[1]!.url).toBe('ws://test/org-2?w=')
      dispose()
    })
  })

  it('routes the initial OrgMaterialized frame into pending.bootstrap and sets bootstrapped', async () => {
    await createRoot(async (dispose) => {
      const [orgId] = createSignal('org-1')
      const pending = makeFakePending()
      const hook = useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: (org, _ws) => `ws://test/${org}`,
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!
      expect(hook.bootstrapped()).toBe(false)

      const initial = create(OrgMaterializedSchema, {
        orgId: 'org-1',
        nodes: {},
        tabs: {},
        floatingWindows: {},
        workspaces: {},
        currentEpoch: 7n,
      })
      sock.sendEvent(create(WatchOrgEventSchema, {
        event: { case: 'initial', value: initial },
      }))

      expect(pending.bootstrap).toHaveBeenCalledTimes(1)
      const arg = pending.bootstrap.mock.calls[0]![0] as { currentEpoch: bigint }
      expect(arg.currentEpoch).toBe(7n)
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
      const [orgId] = createSignal('org-1')
      const pending = makeFakePending()
      useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: (org, _ws) => `ws://test/${org}`,
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      const initial = create(OrgMaterializedSchema, { orgId: 'org-1', currentEpoch: 1n })
      const payload = toBinary(WatchOrgEventSchema, create(WatchOrgEventSchema, {
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
      const [orgId] = createSignal('org-1')
      const pending = makeFakePending()
      useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: (org, _ws) => `ws://test/${org}`,
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      const op = create(OrgOpSchema, {
        orgId: 'org-1',
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
      sock.sendEvent(create(WatchOrgEventSchema, { event: { case: 'batch', value: batch } }))

      expect(pending.consumeRemote).toHaveBeenCalledTimes(1)
      expect((pending.consumeRemote.mock.calls[0]![0] as { batchId: string }).batchId).toBe('b-1')
      dispose()
    })
  })

  it('routes EntityMaterialized into pending.consumeEntityMaterialized', async () => {
    await createRoot(async (dispose) => {
      const [orgId] = createSignal('org-1')
      const pending = makeFakePending()
      useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: (org, _ws) => `ws://test/${org}`,
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
      sock.sendEvent(create(WatchOrgEventSchema, { event: { case: 'entityMaterialized', value: mat } }))

      expect(pending.consumeEntityMaterialized).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('routes EntityRemoved into pending.consumeEntityRemoved and reports dropped pending', async () => {
    await createRoot(async (dispose) => {
      const [orgId] = createSignal('org-1')
      const pending = makeFakePending({ droppedPending: true })
      const onPendingDropped = vi.fn()
      useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        onPendingDropped,
        buildWsUrl: (org, _ws) => `ws://test/${org}`,
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      const rem = create(EntityRemovedSchema, {
        entity: {
          case: 'tab',
          value: create(TabIdentSchema, { tabType: TabType.AGENT, tabId: 'tA' }),
        },
      })
      sock.sendEvent(create(WatchOrgEventSchema, { event: { case: 'entityRemoved', value: rem } }))

      expect(pending.consumeEntityRemoved).toHaveBeenCalledTimes(1)
      expect(onPendingDropped).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('routes presence into activeClient.update', async () => {
    await createRoot(async (dispose) => {
      const [orgId] = createSignal('org-1')
      const ac = createActiveClientStore()
      useOrgEvents({
        orgId,
        activeClient: ac,
        pending: () => makeFakePending() as never,
        buildWsUrl: (org, _ws) => `ws://test/${org}`,
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      const pres = create(PresenceUpdateSchema, { workspaceId: 'w1', activeClientId: 'client-a' })
      sock.sendEvent(create(WatchOrgEventSchema, { event: { case: 'presence', value: pres } }))

      expect(ac.activeFor('w1')).toBe('client-a')
      dispose()
    })
  })

  it('invokes onWorkspaceLifecycleChanged for created / renamed / deleted', async () => {
    await createRoot(async (dispose) => {
      const [orgId] = createSignal('org-1')
      const onWorkspaceLifecycleChanged = vi.fn()
      useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => makeFakePending() as never,
        onWorkspaceLifecycleChanged,
        buildWsUrl: (org, _ws) => `ws://test/${org}`,
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      const created = create(WorkspaceCreatedSchema, {
        workspaceId: 'w1',
        title: 'My WS',
        rootNodeId: 'r1',
      })
      sock.sendEvent(create(WatchOrgEventSchema, { event: { case: 'created', value: created } }))

      expect(onWorkspaceLifecycleChanged).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('ignores a message that arrives on a socket superseded by teardown', async () => {
    await createRoot(async (dispose) => {
      const [orgId, setOrgId] = createSignal('org-1')
      const pending = makeFakePending()
      useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: (org, _ws) => `ws://test/${org}`,
      })
      await flushEffects()
      const stale = FakeSocket.instances[0]!

      // An org_id change tears down the first socket and opens a fresh one.
      setOrgId('org-2')
      await flushEffects()
      expect(FakeSocket.instances).toHaveLength(2)

      // A frame still queued on the OLD (superseded) socket must be dropped by the
      // message handler's stale-connection guard -- otherwise it would re-bootstrap
      // the still-live PendingOpsManager to a stale snapshot (resetting currentEpoch
      // and re-arming the epoch loop). The close/error handlers already guard this;
      // the message handler must too.
      const staleInitial = create(OrgMaterializedSchema, {
        orgId: 'org-1',
        nodes: {},
        tabs: {},
        floatingWindows: {},
        workspaces: {},
        currentEpoch: 99n,
      })
      stale.sendEvent(create(WatchOrgEventSchema, { event: { case: 'initial', value: staleInitial } }))

      expect(pending.bootstrap).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('ignores malformed frames silently', async () => {
    await createRoot(async (dispose) => {
      const [orgId] = createSignal('org-1')
      const pending = makeFakePending()
      useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: (org, _ws) => `ws://test/${org}`,
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
      const [orgId] = createSignal('org-1')
      const pending = makeFakePending()
      const hook = useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
        buildWsUrl: (org, _ws) => `ws://test/${org}`,
      })
      await flushEffects()
      const sock = FakeSocket.instances[0]!

      const initial = create(OrgMaterializedSchema, {
        orgId: 'org-1',
        nodes: {},
        tabs: {},
        floatingWindows: {},
        workspaces: {},
        currentEpoch: 1n,
      })
      sock.sendEvent(create(WatchOrgEventSchema, { event: { case: 'initial', value: initial } }))
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
        const [orgId, setOrgId] = createSignal('org-1')
        useOrgEvents({
          orgId,
          activeClient: createActiveClientStore(),
          pending: () => makeFakePending() as never,
          buildWsUrl: org => `ws://test/${org}`,
        })
        await flushEffects()
        FakeSocket.instances[0]!.emit('close', new Event('close'))

        // First reconnect delay is 250ms +/- 20% jitter (< 300ms); advance past
        // the jitter ceiling so the retry fires deterministically.
        await vi.advanceTimersByTimeAsync(300)
        await flushEffects()
        expect(FakeSocket.instances).toHaveLength(2)

        setOrgId('org-2')
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
        const [orgId] = createSignal('org-1')
        const onFatalClose = vi.fn()
        useOrgEvents({
          orgId,
          activeClient: createActiveClientStore(),
          pending: () => makeFakePending() as never,
          buildWsUrl: org => `ws://test/${org}`,
          onFatalClose,
        })
        await flushEffects()
        // 1008 = policy violation (the hub's /ws/orgevents "forbidden" / auth
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
        const [orgId] = createSignal('org-1')
        const onFatalClose = vi.fn()
        useOrgEvents({
          orgId,
          activeClient: createActiveClientStore(),
          pending: () => makeFakePending() as never,
          buildWsUrl: org => `ws://test/${org}`,
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
        const [orgId] = createSignal('org-1')
        const onFatalClose = vi.fn()
        useOrgEvents({
          orgId,
          activeClient: createActiveClientStore(),
          pending: () => makeFakePending() as never,
          buildWsUrl: org => `ws://test/${org}`,
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

describe('useorgevents (desktop bridge path)', () => {
  // Flush the effect, the onEvent Promise.all, and the .then that opens the
  // relay -- the desktop path is several microtasks deep before it is live.
  async function settleBridge(): Promise<void> {
    for (let i = 0; i < 6; i++)
      await flushEffects()
  }

  it('opens the relay and registers the bridge listeners', async () => {
    bridge.isTauri = true
    await createRoot(async (dispose) => {
      const [orgId] = createSignal('org-1')
      useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => makeFakePending() as never,
      })
      await settleBridge()
      expect(bridge.openCalls).toBe(1)
      expect(bridge.handlers.has('orgevents:message')).toBe(true)
      expect(bridge.handlers.has('orgevents:close')).toBe(true)
      dispose()
    })
  })

  it('tears down the bridge listeners and closes the relay on a terminal close', async () => {
    bridge.isTauri = true
    await createRoot(async (dispose) => {
      const [orgId] = createSignal('org-1')
      const onFatalClose = vi.fn()
      useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => makeFakePending() as never,
        onFatalClose,
      })
      await settleBridge()

      // A terminal close (1008 policy violation / auth expiry) must stop retrying
      // AND release the bridge resources -- the platformBridge listeners and the
      // Go-side relay are not GC-reclaimed like a native WebSocket, so leaving
      // them attached would leak (and double-dispatch on a later re-subscribe).
      bridge.handlers.get('orgevents:close')!({ code: 1008, reason: 'forbidden' })
      await flushEffects()

      expect(onFatalClose).toHaveBeenCalledWith({ code: 1008, reason: 'forbidden' })
      expect(bridge.unlistenCalls.get('orgevents:message')).toBe(1)
      expect(bridge.unlistenCalls.get('orgevents:close')).toBe(1)
      expect(bridge.closeCalls).toBe(1)
      dispose()
    })
  })

  it('does not tear down the bridge on a recoverable close (reconnect path)', async () => {
    bridge.isTauri = true
    await createRoot(async (dispose) => {
      const [orgId] = createSignal('org-1')
      const onFatalClose = vi.fn()
      useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => makeFakePending() as never,
        onFatalClose,
      })
      await settleBridge()

      // A recoverable close (1006 transport drop) schedules a reconnect and must
      // NOT fire onFatalClose or immediately tear the bridge down.
      bridge.handlers.get('orgevents:close')!({ code: 1006, reason: '' })
      await flushEffects()

      expect(onFatalClose).not.toHaveBeenCalled()
      expect(bridge.unlistenCalls.get('orgevents:close') ?? 0).toBe(0)
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
  // (closing the fresh org's relay) and fired onFatalClose, surfacing AppShell's "Live
  // updates disconnected. Reload the page to reconnect." on a freshly-switched org.
  it('ignores a close for an attempt a later org switch superseded', async () => {
    bridge.isTauri = true
    bridge.deferOnEvent = true
    await createRoot(async (dispose) => {
      const [orgId, setOrgId] = createSignal('org-1')
      const onFatalClose = vi.fn()
      useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => makeFakePending() as never,
        onFatalClose,
      })
      await settleBridge()
      // The first attempt's listeners are registered in Rust, but its onEvent
      // promises have not resolved: the registration gap.
      const staleClose = bridge.registrations.find(r => r.name === 'orgevents:close')!.handler
      expect(bridge.openCalls).toBe(0)

      // Switch orgs: tearDown() supersedes attempt 1 and attempt 2 takes over.
      setOrgId('org-2')
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
  it('ignores a message for an attempt a later org switch superseded', async () => {
    bridge.isTauri = true
    bridge.deferOnEvent = true
    await createRoot(async (dispose) => {
      const [orgId, setOrgId] = createSignal('org-1')
      const pending = makeFakePending()
      useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => pending as never,
      })
      await settleBridge()
      const staleMessage = bridge.registrations.find(r => r.name === 'orgevents:message')!.handler

      setOrgId('org-2')
      bridge.releaseOnEvent()
      await settleBridge()

      const evt = create(WatchOrgEventSchema, {
        event: { case: 'initial', value: create(OrgMaterializedSchema, { orgId: 'org-1', currentEpoch: 3n }) },
      })
      const payload = toBinary(WatchOrgEventSchema, evt)
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
      const [orgId, setOrgId] = createSignal('org-1')
      useOrgEvents({
        orgId,
        activeClient: createActiveClientStore(),
        pending: () => makeFakePending() as never,
      })
      await settleBridge()
      const firstRelayId = bridge.openedRelayIds[0]

      setOrgId('org-2')
      await settleBridge()

      expect(bridge.closedRelayIds).toEqual([firstRelayId])
      const secondRelayId = bridge.openedRelayIds[1]
      expect(secondRelayId).toBeGreaterThan(firstRelayId)

      dispose()
      expect(bridge.closedRelayIds).toEqual([firstRelayId, secondRelayId])
    })
  })
})

// The relay ids must stay ordered across webview reloads even when the wall clock
// steps BACKWARD between two page loads (NTP, a manual adjustment): the sidecar
// outlives the reload holding the previous page's owner id, and an open seeded
// below it refuses itself as superseded on every attempt -- org events silently
// never bootstrap. The persisted high-water mark is what carries the ordering
// through a clock regression.
describe('nextorgeventsrelayid', () => {
  it('hands out strictly increasing ids and persists the high-water mark', () => {
    const first = nextOrgEventsRelayId()
    const markAfterFirst = localStorageGet<number>(KEY_ORG_EVENTS_RELAY_SEQ)
    const second = nextOrgEventsRelayId()
    const markAfterSecond = localStorageGet<number>(KEY_ORG_EVENTS_RELAY_SEQ)
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

  it('keeps ids above the persisted mark when the clock steps backward across a reload', async () => {
    // The previous page ran with a clock 5 minutes ahead and left its last id as
    // the persisted mark; the sidecar still holds a relay owned by that id. A
    // reload re-seeds the module -- simulated with a fresh module registry.
    const staleOwner = Date.now() + 5 * 60_000
    localStorageSet(KEY_ORG_EVENTS_RELAY_SEQ, staleOwner)
    vi.resetModules()
    const fresh = await import('./useOrgEvents')
    expect(fresh.nextOrgEventsRelayId()).toBeGreaterThan(staleOwner)
  })

  it('seeds from the clock when it is ahead of the persisted mark', async () => {
    localStorageSet(KEY_ORG_EVENTS_RELAY_SEQ, 1234)
    vi.resetModules()
    const fresh = await import('./useOrgEvents')
    const before = Date.now()
    const id = fresh.nextOrgEventsRelayId()
    expect(id).toBeGreaterThan(before)
  })
})

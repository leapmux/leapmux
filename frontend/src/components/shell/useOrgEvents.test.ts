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
import { createActiveClientStore } from '~/lib/presence/activeClient'
import { useOrgEvents } from './useOrgEvents'

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
})

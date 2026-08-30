import { create } from '@bufbuild/protobuf'
import { createRoot, createSignal } from 'solid-js'
import {
  HLCSchema,
  LWWNodeKindSchema,
  NodeKind,
  NodeRecordSchema,
  WorkspaceContentsRecordSchema,
} from '~/generated/proto/leapmux/v1/user_crdt_pb'
import { HLCClock, PendingOpsManager, setCRDTBridge } from '~/lib/crdt'

/**
 * The identity the whole unit suite runs as, for both the CRDT bridge and the
 * storage namespace (`vitest.setup.ts` signs in as this, and passes no `userId`
 * to `installTestBridge` so that both halves take this one value).
 *
 * Alphanumeric on purpose, so that it reads in a stored key exactly as a real
 * hub id does: ids are 48-character nanoids over `[A-Za-z0-9]`
 * (`internal/util/id`), which `accountStorageKey`'s percent-encoding leaves
 * byte-identical.
 */
export const TEST_USER_ID = 'usertest'

/**
 * installTestBridge creates a PendingOpsManager + bridge wired into
 * the global `~/lib/crdt/bridge` singleton, and seeds the workspace
 * with a single LEAF root node so the projection-driven layout +
 * floating-window stores show a valid initial tile under tests.
 *
 * The bridge installs a Solid signal that bumps on every state-
 * mutating method (the same pattern AppShell uses in production), so
 * memoized projections in the stores re-derive when ops land. The
 * signal is constructed inside `createSignal` — the helper assumes
 * it's called inside a Solid root or a `createRoot`-wrapped test
 * body. (Test files that don't already wrap in `createRoot` will
 * trigger the "computations created outside a root" warning, but
 * the test still functions because the bridge tracks the signal
 * directly through the PendingOpsManager's `notify` callback.)
 */
export interface TestBridgeHandle {
  pending: PendingOpsManager
  clock: HLCClock
  rootTileId: string
  userId: string
  workspaceId: string
  /**
   * One entry per `bridge.flushNow()`, each holding `pendingBatches.length` at
   * the moment of the call.
   *
   * The COUNT AT CALL TIME is the point: a store's unload handler must create
   * its op and then send it, in that order and in one handler, and a growing
   * pending list only proves the first half. Recording the count lets a test
   * pin the ordering without reaching into listener registration.
   */
  flushNowCalls: number[]
  /** Manually unwire — ordinarily the test framework's afterEach handles this. */
  dispose: () => void
}

/**
 * Seed an additional workspace, with its own LEAF root tile, into an already
 * installed harness.
 *
 * `installTestBridge` seeds exactly one workspace, which is all a
 * single-workspace test needs. Anything exercising the point of the projection
 * -- that every workspace is live at once -- needs a second one to move tabs
 * between and to prove a change in one reaches the other.
 */
export function seedWorkspace(
  // Structurally satisfied by `TestBridgeHandle`, so existing callers are
  // unchanged -- and by the bare manager, so `installTestBridge` can seed its
  // own first workspace through here instead of holding a second copy of this
  // body that has to be kept in step by eye.
  harness: { pending: PendingOpsManager },
  workspaceId: string,
  rootTileId: string,
): string {
  harness.pending.state.confirmedState.workspaces[workspaceId] = create(WorkspaceContentsRecordSchema, {
    workspaceId,
    rootNodeId: rootTileId,
  })
  harness.pending.state.confirmedState.nodes[rootTileId] = create(NodeRecordSchema, {
    nodeId: rootTileId,
    parentId: '',
    kind: create(LWWNodeKindSchema, {
      value: NodeKind.LEAF,
      hlc: create(HLCSchema, { physical: 1n, logical: 0n, clientId: 'seed' }),
    }),
  })
  harness.pending.recomputeSpeculative()
  return rootTileId
}

export function installTestBridge(opts?: {
  userId?: string
  workspaceId?: string
  rootTileId?: string
}): TestBridgeHandle {
  const userId = opts?.userId ?? TEST_USER_ID
  const workspaceId = opts?.workspaceId ?? 'ws-test'
  const rootTileId = opts?.rootTileId ?? 'main-tile'
  const ownClient = 'test-client'
  const clock = new HLCClock(ownClient)
  // Reactive version signal so memo-backed consumers re-derive when
  // the manager mutates state in place. Mirrors AppShell's wiring.
  const [version, setVersion] = createSignal(0)
  const bumpVersion = () => setVersion(v => v + 1)
  const pending = new PendingOpsManager(userId, clock, bumpVersion)
  // Seed: workspace contents record + a LEAF root node. The
  // projection's `registeredRoots` lookup will then find the
  // workspace's root and the projected tree will be a single LEAF.
  seedWorkspace({ pending }, workspaceId, rootTileId)
  const flushNowCalls: number[] = []
  setCRDTBridge({
    workspaceId: () => workspaceId,
    enqueue: (batch) => {
      pending.submit(batch)
      return batch.batchId
    },
    // Recorded, not stubbed: the store's unload handler must both CREATE the
    // op and SEND it, and only the second half was ever asserted by accident
    // (a growing pendingBatches proves creation alone). Capturing the pending
    // count AT CALL TIME is what pins the ORDER within the one handler.
    flushNow: () => {
      flushNowCalls.push(pending.state.pendingBatches.length)
    },
    clock: () => clock,
    originClientId: () => ownClient,
    speculativeState: () => {
      // Read the version signal so memos re-derive on every
      // submit/consume call.
      version()
      return pending.state.speculativeState
    },
  })
  return {
    pending,
    clock,
    rootTileId,
    userId,
    workspaceId,
    /** One entry per `bridge.flushNow()`, holding the pending count at that moment. */
    flushNowCalls,
    dispose: () => setCRDTBridge(null),
  }
}

/**
 * Run `body` inside a Solid `createRoot` with a freshly-installed
 * test bridge. The root is disposed when `body` returns (success or
 * throw), tearing down both the Solid reactive scope and the global
 * bridge singleton. Returns whatever `body` returns so test
 * assertions can flow out.
 *
 * If `body` returns a Promise, the dispose is deferred until that
 * Promise settles so async tests (e.g. ones that await a
 * `queueMicrotask` callback before asserting) can still rely on the
 * bridge being wired when the deferred work runs.
 *
 * Every CRDT-bridge unit test needs the same `createRoot((dispose) =>
 * { const harness = installTestBridge(); ...; dispose() })` wrapper.
 * Hiding that boilerplate keeps the test bodies focused on the
 * invariant under test.
 */
export function withTestBridge<T>(
  body: (harness: TestBridgeHandle) => T,
  opts?: Parameters<typeof installTestBridge>[0],
): T {
  return createRoot((dispose) => {
    const harness = installTestBridge(opts)
    let disposed = false
    const safeDispose = () => {
      if (disposed)
        return
      disposed = true
      dispose()
    }
    try {
      const result = body(harness)
      if (result && typeof (result as { then?: unknown }).then === 'function') {
        const promise = result as unknown as Promise<unknown>
        return promise.then(
          (v) => {
            safeDispose()
            return v
          },
          (err) => {
            safeDispose()
            throw err
          },
        ) as unknown as T
      }
      safeDispose()
      return result
    }
    catch (err) {
      safeDispose()
      throw err
    }
  })
}

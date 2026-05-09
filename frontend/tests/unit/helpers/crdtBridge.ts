import { create } from '@bufbuild/protobuf'
import { createRoot, createSignal } from 'solid-js'
import {
  HLCSchema,
  LWWNodeKindSchema,
  NodeKind,
  NodeRecordSchema,
  WorkspaceContentsRecordSchema,
} from '~/generated/leapmux/v1/org_crdt_pb'
import { HLCClock, PendingOpsManager, setCRDTBridge } from '~/lib/crdt'

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
  orgId: string
  workspaceId: string
  /** Manually unwire — ordinarily the test framework's afterEach handles this. */
  dispose: () => void
}

export function installTestBridge(opts?: {
  orgId?: string
  workspaceId?: string
  rootTileId?: string
}): TestBridgeHandle {
  const orgId = opts?.orgId ?? 'org-test'
  const workspaceId = opts?.workspaceId ?? 'ws-test'
  const rootTileId = opts?.rootTileId ?? 'main-tile'
  const ownClient = 'test-client'
  const clock = new HLCClock(ownClient)
  // Reactive version signal so memo-backed consumers re-derive when
  // the manager mutates state in place. Mirrors AppShell's wiring.
  const [version, setVersion] = createSignal(0)
  const bumpVersion = () => setVersion(v => v + 1)
  const pending = new PendingOpsManager(orgId, clock, bumpVersion)
  // Seed: workspace contents record + a LEAF root node. The
  // projection's `registeredRoots` lookup will then find the
  // workspace's root and the projected tree will be a single LEAF.
  pending.state.confirmedState.workspaces[workspaceId] = create(WorkspaceContentsRecordSchema, {
    workspaceId,
    rootNodeId: rootTileId,
  })
  pending.state.confirmedState.nodes[rootTileId] = create(NodeRecordSchema, {
    nodeId: rootTileId,
    parentId: '',
    kind: create(LWWNodeKindSchema, {
      value: NodeKind.LEAF,
      hlc: create(HLCSchema, { physical: 1n, logical: 0n, clientId: 'seed' }),
    }),
  })
  pending.recomputeSpeculative()
  setCRDTBridge({
    orgId: () => orgId,
    workspaceId: () => workspaceId,
    enqueue: (batch) => {
      pending.submit(batch)
      return batch.batchId
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
    orgId,
    workspaceId,
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

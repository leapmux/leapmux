import type { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { first } from '~/lib/lexorank'
import { ctxFromBridge, getCRDTBridge, newBatch, setTabPosition, setTabTileId, setTabWorkerId } from './index'

/**
 * Result of `seedTabIntoNewWorkspace`. `rootNodeId` is the new
 * workspace's seed-root LEAF the seed batch placed the tab under;
 * the caller uses it to pre-seed per-workspace stores (registry
 * snapshot, tabStore tileId) so the freshly-created workspace
 * renders with the new tab immediately, instead of waiting for the
 * CRDT-projection reconciler — which would re-insert the tab with
 * only its CRDT-driven fields (tile_id, position, worker_id) and
 * none of the agent metadata (title, agentProvider, …) the caller
 * already has from the OpenAgent response.
 *
 * `position` is the LexoRank the seed batch used. Tab list ordering
 * needs to match what other clients will project from the CRDT.
 */
export interface SeedTabResult {
  rootNodeId: string
  position: string
}

/**
 * Seed-tab batch for a freshly-created workspace. The plan's lifecycle
 * flow is:
 *
 *   1. Client calls `WorkspaceService.CreateWorkspace`.
 *   2. Hub commits the `workspaces` row plus a lifecycle-outbox row.
 *   3. The org-CRDT manager processes the outbox: seeds the root
 *      NodeRecord, populates `WorkspaceContentsRecord.root_node_id`,
 *      and broadcasts `WorkspaceCreated{workspace_id, title,
 *      root_node_id}`.
 *   4. The frontend that initiated the create reads the root_node_id
 *      and submits its seed-tab batch — `SetTabRegister(tile_id=root) +
 *      SetTabRegister(position=…)` + optional `SetTabRegister(worker_id=
 *      …)`.
 *
 * `seedTabIntoNewWorkspace` implements step 4: it awaits the
 * `leapmux:workspace-created` window event dispatched by
 * `useOrgEvents` when the hub's `WorkspaceCreated` broadcast lands,
 * then enqueues the seed batch via the bridge. If the workspace is
 * already in the speculative state when called (the event fired
 * before the caller reached us), it proceeds immediately. The
 * `timeoutMs` cap bounds the worst-case wait when the outbox drain
 * is unusually slow.
 *
 * Returns `{ rootNodeId, position }` when the seed batch was enqueued;
 * `null` when the bridge was unwired or the timeout elapsed. Callers
 * can warn-toast or retry on `null`.
 */
export async function seedTabIntoNewWorkspace(args: {
  workspaceId: string
  tabType: TabType
  tabId: string
  workerId?: string
  timeoutMs?: number
}): Promise<SeedTabResult | null> {
  const bridge = getCRDTBridge()
  if (!bridge)
    return null
  const ctx = ctxFromBridge(bridge)
  if (!ctx)
    return null

  const rootNodeId = await awaitWorkspaceRoot(bridge, args.workspaceId, args.timeoutMs ?? 5000)
  if (!rootNodeId)
    return null

  const position = first()
  const ops = [
    setTabTileId(ctx, args.tabType, args.tabId, rootNodeId),
    setTabPosition(ctx, args.tabType, args.tabId, position),
  ]
  if (args.workerId)
    ops.push(setTabWorkerId(ctx, args.tabType, args.tabId, args.workerId))
  bridge.enqueue(newBatch(ops))
  return { rootNodeId, position }
}

// awaitWorkspaceRoot resolves to the workspace's root_node_id as soon
// as either the speculative state already has it (the
// WorkspaceCreated event was processed before this call) OR a
// matching `leapmux:workspace-created` event arrives within the
// timeout. The combination keeps the race-free contract without
// polling.
function awaitWorkspaceRoot(
  bridge: NonNullable<ReturnType<typeof getCRDTBridge>>,
  workspaceId: string,
  timeoutMs: number,
): Promise<string | null> {
  const existing = bridge.speculativeState()?.workspaces[workspaceId]?.rootNodeId
  if (existing)
    return Promise.resolve(existing)
  if (typeof window === 'undefined')
    return Promise.resolve(null)

  return new Promise<string | null>((resolve) => {
    let settled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    function finish(value: string | null): void {
      if (settled)
        return
      settled = true
      window.removeEventListener('leapmux:workspace-created', handler)
      if (timer !== undefined)
        clearTimeout(timer)
      resolve(value)
    }
    function handler(evt: Event): void {
      const detail = (evt as CustomEvent<{ workspaceId: string, rootNodeId: string }>).detail
      if (detail?.workspaceId !== workspaceId)
        return
      if (detail.rootNodeId)
        finish(detail.rootNodeId)
    }
    // Register the listener BEFORE reading speculative state so we
    // can't miss an event that fires between the two — eliminates the
    // need for the prior "late re-check" branch.
    window.addEventListener('leapmux:workspace-created', handler)
    timer = setTimeout(finish, timeoutMs, null)
    const current = bridge.speculativeState()?.workspaces[workspaceId]?.rootNodeId
    if (current)
      finish(current)
  })
}

import type { WorkspaceTab } from '~/generated/leapmux/v1/workspace_pb'
import { createInflightCache } from '~/lib/inflightCache'
import { workspaceClient } from './clients'

interface Batch {
  orgId: string
  resolvers: Map<string, {
    resolve: (value: { tabs: WorkspaceTab[] }) => void
    reject: (reason: unknown) => void
  }>
}

const pendingBatches = new Map<string, Batch>()
const inflight = createInflightCache<string, { tabs: WorkspaceTab[] }>()

/**
 * Fetches the tabs for a single workspace, coalescing concurrent calls into
 * one ListTabs RPC per org. Each caller receives only the tabs belonging to
 * the workspace it asked for.
 */
export function listTabsForWorkspace(orgId: string, workspaceId: string): Promise<{ tabs: WorkspaceTab[] }> {
  return inflight.run(`${orgId}:${workspaceId}`, () => {
    const batch = pendingBatches.get(orgId) ?? createBatch(orgId)
    return new Promise((resolve, reject) => {
      batch.resolvers.set(workspaceId, { resolve, reject })
    })
  })
}

function createBatch(orgId: string): Batch {
  const batch: Batch = { orgId, resolvers: new Map() }
  pendingBatches.set(orgId, batch)
  queueMicrotask(() => {
    // Remove first so any call that arrives during the RPC opens a fresh batch.
    pendingBatches.delete(orgId)
    void flushBatch(batch)
  })
  return batch
}

async function flushBatch(batch: Batch): Promise<void> {
  try {
    const resp = await workspaceClient.listTabs({
      orgId: batch.orgId,
      workspaceIds: Array.from(batch.resolvers.keys()),
    })
    const byWorkspace = new Map<string, WorkspaceTab[]>()
    for (const tab of resp.tabs) {
      const list = byWorkspace.get(tab.workspaceId)
      if (list)
        list.push(tab)
      else
        byWorkspace.set(tab.workspaceId, [tab])
    }
    for (const [wsId, waiter] of batch.resolvers) {
      waiter.resolve({ tabs: byWorkspace.get(wsId) ?? [] })
    }
  }
  catch (err) {
    for (const waiter of batch.resolvers.values())
      waiter.reject(err)
  }
}

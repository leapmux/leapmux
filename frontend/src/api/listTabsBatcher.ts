import type { WorkspaceTab } from '~/generated/leapmux/v1/workspace_pb'
import { createInflightCache } from '~/lib/inflightCache'
import { workspaceClient } from './clients'

interface Batch {
  resolvers: Map<string, {
    resolve: (value: { tabs: WorkspaceTab[] }) => void
    reject: (reason: unknown) => void
  }>
}

let pendingBatch: Batch | null = null
const inflight = createInflightCache<string, { tabs: WorkspaceTab[] }>()

/**
 * Fetches the tabs for a single workspace, coalescing concurrent calls into
 * one ListTabs RPC. Each caller receives only the tabs belonging to
 * the workspace it asked for.
 */
export function listTabsForWorkspace(workspaceId: string): Promise<{ tabs: WorkspaceTab[] }> {
  return inflight.run(workspaceId, () => {
    const batch = pendingBatch ?? createBatch()
    return new Promise((resolve, reject) => {
      batch.resolvers.set(workspaceId, { resolve, reject })
    })
  })
}

function createBatch(): Batch {
  const batch: Batch = { resolvers: new Map() }
  pendingBatch = batch
  queueMicrotask(() => {
    // Remove first so any call that arrives during the RPC opens a fresh batch.
    if (pendingBatch === batch)
      pendingBatch = null
    void flushBatch(batch)
  })
  return batch
}

async function flushBatch(batch: Batch): Promise<void> {
  try {
    const resp = await workspaceClient.listTabs({
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

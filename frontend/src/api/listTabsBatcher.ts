import type { Client } from '@connectrpc/connect'
import type { WorkspaceService, WorkspaceTab } from '~/generated/leapmux/v1/workspace_pb'
import { workspaceClient as defaultWorkspaceClient } from './clients'

interface Batch {
  orgId: string
  ids: Set<string>
  waiters: Map<string, Array<{
    resolve: (value: { tabs: WorkspaceTab[] }) => void
    reject: (reason: unknown) => void
  }>>
}

const pendingBatches = new Map<string, Batch>()

let workspaceClient: Client<typeof WorkspaceService> = defaultWorkspaceClient

// Exposed for tests; production always uses the real client.
export function __setListTabsClientForTesting(client: Client<typeof WorkspaceService> | null): void {
  workspaceClient = client ?? defaultWorkspaceClient
  pendingBatches.clear()
}

/**
 * Fetches the tabs for a single workspace, coalescing concurrent calls into
 * one ListTabs RPC per org. Each caller receives only the tabs belonging to
 * the workspace it asked for.
 */
export function listTabsForWorkspace(orgId: string, workspaceId: string): Promise<{ tabs: WorkspaceTab[] }> {
  const batch = pendingBatches.get(orgId) ?? createBatch(orgId)
  batch.ids.add(workspaceId)
  return new Promise((resolve, reject) => {
    const list = batch.waiters.get(workspaceId) ?? []
    list.push({ resolve, reject })
    batch.waiters.set(workspaceId, list)
  })
}

function createBatch(orgId: string): Batch {
  const batch: Batch = { orgId, ids: new Set(), waiters: new Map() }
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
      workspaceIds: Array.from(batch.ids),
    })
    const byWorkspace = new Map<string, WorkspaceTab[]>()
    for (const id of batch.ids)
      byWorkspace.set(id, [])
    for (const tab of resp.tabs) {
      const list = byWorkspace.get(tab.workspaceId)
      if (list)
        list.push(tab)
    }
    for (const [wsId, waiters] of batch.waiters) {
      const tabs = byWorkspace.get(wsId) ?? []
      for (const w of waiters)
        w.resolve({ tabs })
    }
  }
  catch (err) {
    for (const waiters of batch.waiters.values()) {
      for (const w of waiters)
        w.reject(err)
    }
  }
}

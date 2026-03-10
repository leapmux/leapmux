import type { LayoutStoreState } from './layout.store'
import type { TabStoreState } from './tab.store'
import type { TerminalInfo } from './terminal.store'
import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import { createSignal } from 'solid-js'

/**
 * A snapshot of per-workspace state, cached so that switching back to a
 * previously visited workspace restores instantly without re-fetching.
 */
export interface WorkspaceSnapshot {
  workspaceId: string
  tabs: TabStoreState
  layout: LayoutStoreState
  agents: AgentInfo[]
  terminals: TerminalInfo[]
  restored: boolean
  tabsLoaded: boolean
}

export function createWorkspaceStoreRegistry() {
  const snapshots = new Map<string, WorkspaceSnapshot>()
  // Reactive version signal — bumped on every mutation so that reads
  // within reactive contexts (components, effects) re-evaluate.
  const [version, setVersion] = createSignal(0)

  function get(workspaceId: string): WorkspaceSnapshot | undefined {
    version() // track reactive dependency
    return snapshots.get(workspaceId)
  }

  function set(workspaceId: string, snapshot: WorkspaceSnapshot): void {
    snapshots.set(workspaceId, snapshot)
    setVersion(v => v + 1)
  }

  function remove(workspaceId: string): void {
    snapshots.delete(workspaceId)
    setVersion(v => v + 1)
  }

  function has(workspaceId: string): boolean {
    version() // track reactive dependency
    return snapshots.has(workspaceId)
  }

  function allIds(): string[] {
    version() // track reactive dependency
    return [...snapshots.keys()]
  }

  function all(): WorkspaceSnapshot[] {
    version() // track reactive dependency
    return [...snapshots.values()]
  }

  return { get, set, remove, has, allIds, all }
}

export type WorkspaceStoreRegistryType = ReturnType<typeof createWorkspaceStoreRegistry>

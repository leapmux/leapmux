import type { FloatingWindowStoreState } from './floatingWindow.store'
import type { LayoutStoreState } from './layout.store'
import type { RestorableTabState, Tab } from './tab.store'
import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import { createSignal } from 'solid-js'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { tabKey } from './tab.store'

/**
 * A snapshot of per-workspace state, cached so that switching back to a
 * previously visited workspace restores instantly without re-fetching.
 */
export interface WorkspaceSnapshot extends RestorableTabState {
  workspaceId: string
  layout: LayoutStoreState
  floatingWindows?: FloatingWindowStoreState
  agents: AgentInfo[]
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

  /**
   * Apply a patch to an existing snapshot. No-op if the workspace has no
   * snapshot, or if the patcher returns the current snapshot unchanged
   * (same reference) — lets callers short-circuit without invalidating
   * reactive consumers.
   */
  function update(workspaceId: string, patch: (snap: WorkspaceSnapshot) => WorkspaceSnapshot): void {
    const current = snapshots.get(workspaceId)
    if (!current)
      return
    const next = patch(current)
    if (next === current)
      return
    snapshots.set(workspaceId, next)
    setVersion(v => v + 1)
  }

  function all(): WorkspaceSnapshot[] {
    version() // track reactive dependency
    return [...snapshots.values()]
  }

  /** First snapshot matching `predicate`, without materializing the full array. */
  function findContaining(predicate: (snap: WorkspaceSnapshot) => boolean): WorkspaceSnapshot | undefined {
    version() // track reactive dependency
    for (const snap of snapshots.values()) {
      if (predicate(snap))
        return snap
    }
    return undefined
  }

  /**
   * Remove a tab from a snapshot. For AGENT tabs, also drops the matching
   * agent record so the snapshot stays consistent. No-op if the snapshot or
   * the tab is missing.
   */
  function removeTab(workspaceId: string, tab: Tab): void {
    const key = tabKey(tab)
    update(workspaceId, (snap) => {
      const nextTabs = snap.tabs.filter(t => tabKey(t) !== key)
      if (nextTabs.length === snap.tabs.length)
        return snap
      const nextAgents = tab.type === TabType.AGENT
        ? snap.agents.filter(a => a.id !== tab.id)
        : snap.agents
      return { ...snap, tabs: nextTabs, agents: nextAgents }
    })
  }

  return { get, set, update, all, findContaining, removeTab }
}

export type WorkspaceStoreRegistryType = ReturnType<typeof createWorkspaceStoreRegistry>

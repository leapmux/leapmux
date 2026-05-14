import type { LayoutStoreState } from './layout.store'
import type { RestorableTabState, Tab } from '~/stores/tab.types'
import { createSignal } from 'solid-js'
import { tabKey } from '~/stores/tab.helpers'

/**
 * A snapshot of per-workspace state, cached so that switching back to a
 * previously visited workspace restores instantly without re-fetching.
 *
 * Per-agent metadata lives directly on the `tabs` records (populated by
 * `protoToAgentTabFields` on hydration), so there is no separate
 * `agents` slot to keep in sync. Floating-window state is omitted too:
 * `floatingWindowStore` is projection-driven from the CRDT bridge, so
 * re-activation re-derives it from the materialized state directly.
 */
export interface WorkspaceSnapshot extends RestorableTabState {
  workspaceId: string
  layout: LayoutStoreState
  restored: boolean
  tabsLoaded: boolean
}

/**
 * Build a fresh empty workspace snapshot. The cross-workspace move path
 * (useCrossWorkspaceMove) creates one when dropping a tab onto a
 * workspace the client has never opened — neither `restored` nor
 * `tabsLoaded` are set so the subsequent ListTabs fetch on switch-in
 * still merges the hub's authoritative tab list.
 */
export function createEmptySnapshot(
  workspaceId: string,
  opts?: { layoutRootId?: string },
): WorkspaceSnapshot {
  const tileId = opts?.layoutRootId ?? 'tile-1'
  return {
    workspaceId,
    tabs: [],
    activeTabKey: null,
    layout: {
      root: { type: 'leaf', id: tileId },
      focusedTileId: tileId,
    },
    restored: false,
    tabsLoaded: false,
  }
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
    // Same-reference replays (double-mount, idempotent restore) MUST
    // NOT bump the version signal — every reactive consumer would re-run
    // for no observable change.
    if (snapshots.get(workspaceId) === snapshot)
      return
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
   * Remove a tab from a snapshot. No-op if the snapshot or the tab is
   * missing.
   */
  function removeTab(workspaceId: string, tab: Tab): void {
    const key = tabKey(tab)
    update(workspaceId, (snap) => {
      const nextTabs = snap.tabs.filter(t => tabKey(t) !== key)
      if (nextTabs.length === snap.tabs.length)
        return snap
      return { ...snap, tabs: nextTabs }
    })
  }

  return { get, set, update, all, findContaining, removeTab }
}

export type WorkspaceStoreRegistryType = ReturnType<typeof createWorkspaceStoreRegistry>

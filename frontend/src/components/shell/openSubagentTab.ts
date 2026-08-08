import type { createLayoutStore } from '~/stores/layout.store'
import type { TabMetadata, TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { parseTabKey, resolveOptimisticGitInfo, tabKey } from '~/stores/tab.helpers'
import { emitAddTab, emitReviveTab, positionAfterKey } from '~/stores/tabOps'

/**
 * Open (or activate, or revive) a subagent's tab from a Background tasks row.
 *
 * A subagent tab's id == the child agent id. Three outcomes:
 *
 *   1. Already open -> activate it + focus its tile ('activated').
 *   2. Tombstoned (closed before) -> revive it via a revive+re-placement batch
 *      so the tab re-opens ('revived'). Tab tombstones are permanent without a
 *      revive op; see Part 5b.
 *   3. New -> place it adjacent to its parent tab (or the focused tile when the
 *      parent is absent/unplaced) and select it ('opened').
 *
 * Returns 'no-tile' when no projected tile is resolvable (the same refusal
 * openTabInFocusedTile uses: emitting against a placeholder tile is rejected by
 * the hub and orphans the resource).
 *
 * The metadata patch runs BEFORE the emit (the documented order rule): emit*Tab
 * applies to speculativeState synchronously, so the projection renders the tab
 * before this returns; patching afterwards paints an untitled tab for a frame.
 */
export interface OpenSubagentTabDeps {
  view: TabView
  layoutStore: ReturnType<typeof createLayoutStore>
  selection: TabSelectionStore
  metadata: TabMetadataStore
  /** Revive-capable CRDT bridge accessor; null when unwired (tests). */
  speculativeTabs: () => Record<string, { tombstoneAt?: unknown } | undefined>
  focusTileId?: (tileId: string) => void
}

export type OpenSubagentTabResult = 'activated' | 'opened' | 'revived' | 'no-tile'

export function openSubagentTab(
  deps: OpenSubagentTabDeps,
  item: { childAgentId?: string, parentAgentId?: string, title?: string },
): OpenSubagentTabResult {
  const childAgentId = item.childAgentId
  if (!childAgentId)
    return 'no-tile'

  const { view, layoutStore, selection } = deps

  // 1. Already open -> activate + focus.
  const existing = view.getAgentTab(childAgentId)
  if (existing) {
    selection.setActiveById(TabType.AGENT, childAgentId)
    if (existing.tileId)
      deps.focusTileId?.(existing.tileId)
    return 'activated'
  }

  // 2. Tombstoned -> revive + re-placement batch.
  const spec = deps.speculativeTabs()[childAgentId]
  if (spec && spec.tombstoneAt != null) {
    const tileId = parentTileId(deps, item) ?? focusedTile(layoutStore)
    if (!tileId || !layoutStore.hasProjectedTile(tileId))
      return 'no-tile'
    const workerId = parentWorkerId(deps, item)
    if (!workerId)
      return 'no-tile'
    return placeChildTab(deps, item, childAgentId, tileId, workerId, emitReviveTab, 'revived')
  }

  // 3. New -> place next to the parent (or focused tile fallback).
  const parentTile = parentTileId(deps, item)
  const tileId = parentTile && layoutStore.hasProjectedTile(parentTile)
    ? parentTile
    : focusedTile(layoutStore)
  if (!tileId || !layoutStore.hasProjectedTile(tileId))
    return 'no-tile'
  // parentAgentId is empty for a root-level subagent (Claude/ACP never set it;
  // it means "the owner is the parent"). Fall back to the active tab in the
  // target tile, which is the root owner that spawned the child.
  const workerId = parentWorkerId(deps, item) ?? activeWorkerIdForTile(deps, tileId)
  if (!workerId)
    return 'no-tile'

  return placeChildTab(deps, item, childAgentId, tileId, workerId, emitAddTab, 'opened')
}

/** The tab-object shape both emitAddTab and emitReviveTab accept. */
type EmitTabFn = (tab: {
  type: TabType
  id: string
  tileId: string
  position: string
  workerId: string
}) => string | null

/**
 * The shared placement tail for the revive and open branches: patch metadata,
 * emit the tab op (add or revive), activate the tab, and focus its tile. The
 * activate branch (outcome 1) does not place, so it bypasses this helper.
 */
function placeChildTab(
  deps: OpenSubagentTabDeps,
  item: { parentAgentId?: string, title?: string },
  childAgentId: string,
  tileId: string,
  workerId: string,
  emit: EmitTabFn,
  label: 'opened' | 'revived',
): OpenSubagentTabResult {
  const { view, selection, metadata } = deps
  metadata.patch(childAgentId, buildMetadata(deps, item))
  emit({
    type: TabType.AGENT,
    id: childAgentId,
    tileId,
    position: positionAfterKey(view.forTile(tileId), parentKey(deps, item, tileId)),
    workerId,
  })
  selection.setActiveById(TabType.AGENT, childAgentId)
  deps.focusTileId?.(tileId)
  return label
}

function focusedTile(layoutStore: OpenSubagentTabDeps['layoutStore']): string {
  return layoutStore.focusedTileId()
}

function parentTileId(deps: OpenSubagentTabDeps, item: { parentAgentId?: string }): string | undefined {
  if (!item.parentAgentId)
    return undefined
  const parent = deps.view.getAgentTab(item.parentAgentId)
  return parent?.tileId
}

function parentWorkerId(deps: OpenSubagentTabDeps, item: { parentAgentId?: string }): string | undefined {
  if (item.parentAgentId) {
    const parent = deps.view.getAgentTab(item.parentAgentId)
    if (parent?.workerId)
      return parent.workerId
  }
  return undefined
}

// activeWorkerIdForTile resolves the workerId of the currently-active AGENT tab
// in tileId. Used as the fallback when a registry row carries no parentAgentId
// (Claude/ACP never set it -- the owner IS the parent), so the child tab still
// lands on the owner's worker.
function activeWorkerIdForTile(deps: OpenSubagentTabDeps, tileId: string): string | undefined {
  const key = deps.selection.activeKeyForTile(tileId)
  if (!key)
    return undefined
  const parsed = parseTabKey(key)
  if (!parsed || parsed.type !== TabType.AGENT)
    return undefined
  return deps.view.getAgentTab(parsed.id)?.workerId
}

function parentKey(deps: OpenSubagentTabDeps, item: { parentAgentId?: string }, tileId: string): string | undefined {
  if (!item.parentAgentId)
    return deps.selection.activeKeyForTile(tileId) ?? undefined
  return tabKey({ type: TabType.AGENT, id: item.parentAgentId })
}

function buildMetadata(
  deps: OpenSubagentTabDeps,
  item: { parentAgentId?: string, title?: string },
): TabMetadata {
  const parent = item.parentAgentId ? deps.view.getAgentTab(item.parentAgentId) : undefined
  // workerId and parentAgentId are NOT metadata fields: workerId lives on the
  // projection (the emit*Tab op carries it), and parentAgentId is hydrated by
  // listAgents. Seed only title/workingDir/agentProvider + the optimistic git
  // fields the sidebar groups by until hydration lands.
  const git = parent ? resolveOptimisticGitInfo(parent, { workingDir: parent.workingDir }) : undefined
  return {
    title: item.title || undefined,
    workingDir: parent?.workingDir,
    agentProvider: parent?.agentProvider,
    ...git,
  }
}

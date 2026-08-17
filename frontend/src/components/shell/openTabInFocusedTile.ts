import type { TabType } from '~/generated/leapmux/v1/workspace_pb'
import type { createLayoutStore } from '~/stores/layout.store'
import type { TabMetadata, TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { emitAddTab, positionAfterKey } from '~/stores/tabOps'

/**
 * Place a newly-opened tab on the focused tile and select it.
 *
 * Every "open a tab" path does the same five steps in the same order, and the
 * ORDER is the part worth having in one place:
 *
 *   1. resolve the placement tile (the focused tile, or the first main
 *      leaf when focus sits on a floating-window tile),
 *   2. read that tile's current selection, so the new tab lands next to it
 *      rather than at the end of the strip,
 *   3. write metadata FIRST — `emitAddTab` applies to `speculativeState`
 *      synchronously, so the projection renders the tab before this function
 *      returns; patching afterwards paints an untitled tab for a frame,
 *   4. emit the placement op (which IS the placement — there is no local copy),
 *   5. select it.
 *
 * Callers differ only in what they compute BEFOREHAND (an optimistic git seed,
 * a file's view mode) and what they do AFTERWARDS (focus the editor, register
 * an E2EE path with rollback). Neither belongs here.
 */
export interface OpenTabDeps {
  view: TabView
  layoutStore: ReturnType<typeof createLayoutStore>
  selection: TabSelectionStore
  metadata: TabMetadataStore
}

export interface OpenTabPlacement {
  type: TabType
  id: string
  workerId?: string
}

/**
 * Whether `openTabInFocusedTile` would place a tab right now: the
 * workspace's tree really arrived in the projection. Focus does not enter
 * the question — placement resolves to the first main leaf when the
 * focused tile is a floating-window tile (`placementTileId`).
 *
 * Every path that creates a worker-side resource — the quick actions, the
 * New Agent / New Terminal dialogs, the Change-branch worktree flow, and
 * file opens — consults this BEFORE the worker RPC, because the RPC is
 * the step that cannot be taken back: a placement refusal after it leaves
 * an orphaned agent or pty behind with no tab to reach it by.
 * `openTabInFocusedTile` keeps its own refusal as well (the predicate
 * becoming false between the check and the call is always possible),
 * making the orphan class impossible rather than merely unreached.
 */
export function hasPlaceableTab(
  layoutStore: ReturnType<typeof createLayoutStore>,
): boolean {
  return layoutStore.placementTileId() !== ''
}

/**
 * Returns the tile the tab was placed on, or `''` when there is no real
 * tile to place it on.
 *
 * The empty answer is a refusal, not a failure mode to ignore: while the
 * projection has no tree for this workspace, placement resolves to the
 * locally-minted placeholder leaf. Emitting `AddTab` against that names a
 * node the hub has never heard of, and it rejects the batch — after the
 * caller has already created the agent or terminal on the worker, so the
 * result is an orphaned resource with no tab.
 */
export function openTabInFocusedTile(
  deps: OpenTabDeps,
  tab: OpenTabPlacement,
  metadataFields: TabMetadata,
): string {
  const tileId = deps.layoutStore.placementTileId()
  if (!tileId)
    return ''
  const afterKey = deps.selection.activeKeyForTile(tileId)
  deps.metadata.patch(tab.id, metadataFields)
  emitAddTab({
    type: tab.type,
    id: tab.id,
    tileId,
    position: positionAfterKey(deps.view.forTile(tileId), afterKey ?? undefined),
    workerId: tab.workerId,
  })
  deps.selection.setActiveById(tab.type, tab.id)
  return tileId
}

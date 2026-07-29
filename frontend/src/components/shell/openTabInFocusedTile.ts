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
 *   1. resolve the focused tile,
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
 * Returns the tile the tab was placed on, or `''` when there is no real tile to
 * place it on.
 *
 * The empty answer is a refusal, not a failure mode to ignore: `focusedTileId`
 * falls back to the first leaf of `projectedRoot()`, which is a LOCALLY-MINTED
 * placeholder while the projection has no tree for this workspace. Emitting
 * `AddTab` against that names a node the hub has never heard of, and it rejects
 * the batch -- after the caller has already created the agent or terminal on the
 * worker, so the result is an orphaned resource with no tab. Callers already
 * check `workspaceReady` before offering the affordance; this makes the class
 * impossible rather than merely unreached.
 */
export function openTabInFocusedTile(
  deps: OpenTabDeps,
  tab: OpenTabPlacement,
  metadataFields: TabMetadata,
): string {
  const tileId = deps.layoutStore.focusedTileId()
  if (!tileId || !deps.layoutStore.hasProjectedTile(tileId))
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

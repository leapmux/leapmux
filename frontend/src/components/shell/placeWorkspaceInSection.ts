import { workspaceClient } from '~/api/clients'
import { showWarnToast } from '~/components/common/Toast'
import { WorkspaceArchiveState } from '~/generated/proto/leapmux/v1/common_pb'
import { appendPosition } from '~/lib/lexorank'

/**
 * What has to happen after CreateWorkspace when the user opened the dialog
 * from a section menu. Refresh workspace and section metadata before local
 * activation, then refresh both again after the section move settles.
 *
 * `targetSectionId` null means the shortcut path (no preselection), which
 * refreshes both lists once. The workspace
 * already lives in the default one.
 *
 * A failed move warn-toasts rather than passing silently. The two sibling
 * copies of this same RPC in `useWorkspaceOperations` ("Failed to move
 * workspace", "Failed to reorder workspace") already did; this one swallowed
 * its rejection, so a workspace could land in the default section instead of
 * the section the user clicked, with nothing on screen to say so.
 *
 * The first workspace load protects a just-published selection while its HTTP
 * metadata is still missing. The final section load observes the move, which
 * emits no lifecycle event. The owned loaders reject older list responses.
 *
 * Extracted from a closure inline in an `AppShellDialogs` JSX prop, where it
 * combined lexorank math, an RPC, a refresh and navigation in one 25-line
 * block reachable only by rendering the dialog. `handleBranchChanged` is the
 * same seam, taken from the same position for the same reason.
 */
export interface PlaceWorkspaceDeps {
  isCurrent: () => boolean
  /**
   * Narrowed to the one field `appendPosition` reads, rather than the whole
   * `SectionItem`. The real store satisfies it structurally, and a test can
   * supply a two-field fixture instead of a full section item.
   */
  sectionStore: { getItemsForSection: (sectionId: string) => readonly { position: string }[] }
  loadWorkspaces: () => Promise<void>
  loadSections: () => Promise<void>
}

export function placeWorkspaceInSection(
  deps: PlaceWorkspaceDeps,
  workspaceId: string,
  targetSectionId: string | null,
): void {
  if (!deps.isCurrent())
    return
  const refreshLists = () => {
    if (!deps.isCurrent())
      return
    void deps.loadWorkspaces()
    void deps.loadSections()
  }
  if (!targetSectionId) {
    refreshLists()
    return
  }
  // Append past the section's existing items so the new workspace gets a
  // unique lexorank rather than colliding with whichever item already sits
  // at the head.
  const position = appendPosition(deps.sectionStore.getItemsForSection(targetSectionId))
  refreshLists()
  // CreateWorkspace just made this workspace, and a new workspace is active.
  // The Hub refuses the move if another client archived it meanwhile.
  workspaceClient.moveWorkspace({
    workspaceId,
    sectionId: targetSectionId,
    position,
    expectedArchiveState: WorkspaceArchiveState.ACTIVE,
  })
    .catch((err) => {
      if (deps.isCurrent())
        showWarnToast('Failed to move the workspace into its section', err)
    })
    .finally(refreshLists)
}

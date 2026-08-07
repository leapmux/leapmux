import { sectionClient } from '~/api/clients'
import { showWarnToast } from '~/components/common/Toast'
import { appendPosition } from '~/lib/lexorank'

/**
 * What has to happen after CreateWorkspace when the user opened the dialog
 * from a specific section's "+" button: move the fresh workspace into that
 * section, then refresh the workspace list.
 *
 * `targetSectionId` null means the shortcut path (no preselection), which
 * only refreshes — there is no section to move into, and the workspace
 * already lives in the default one.
 *
 * A failed move warn-toasts rather than passing silently. The two sibling
 * copies of this same RPC in `useWorkspaceOperations` ("Failed to move
 * workspace", "Failed to reorder workspace") already did; this one swallowed
 * its rejection, so a workspace could land in the default section instead of
 * the section the user clicked, with nothing on screen to say so.
 *
 * The refresh runs on both paths. The move is fire-and-forget by design: the
 * caller navigates to the new workspace immediately, and the section
 * placement converges on the refresh rather than blocking that navigation.
 *
 * Extracted from a closure inline in an `AppShellDialogs` JSX prop, where it
 * combined lexorank math, an RPC, a refresh and navigation in one 25-line
 * block reachable only by rendering the dialog. `handleBranchChanged` is the
 * same seam, taken from the same position for the same reason.
 */
export interface PlaceWorkspaceDeps {
  /**
   * Narrowed to the one field `appendPosition` reads, rather than the whole
   * `SectionItem`. The real store satisfies it structurally, and a test can
   * supply a two-field fixture instead of a full section item.
   */
  sectionStore: { getItemsForSection: (sectionId: string) => readonly { position: string }[] }
  loadWorkspaces: () => Promise<void>
}

export function placeWorkspaceInSection(
  deps: PlaceWorkspaceDeps,
  workspaceId: string,
  targetSectionId: string | null,
): void {
  if (!targetSectionId) {
    void deps.loadWorkspaces()
    return
  }
  // Append past the section's existing items so the new workspace gets a
  // unique lexorank rather than colliding with whichever item already sits
  // at the head.
  const position = appendPosition(deps.sectionStore.getItemsForSection(targetSectionId))
  sectionClient.moveWorkspace({ workspaceId, sectionId: targetSectionId, position })
    .catch((err) => {
      showWarnToast('Failed to move the workspace into its section', err)
    })
    .finally(() => {
      void deps.loadWorkspaces()
    })
}

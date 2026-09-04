import { setWorkspacesExpanded } from './expandedWorkspaces'

/**
 * Bring one workspace row into view, with its tab tree open.
 *
 * A DOM read, deliberately: the row lives inside a scroll container the sidebar
 * owns, several components below whoever asks for the reveal, and threading a
 * ref up through the section list to serve one menu item would put a second
 * handle on every row.
 *
 * ONE node, so `querySelector` rather than a loop over every match. `AppShell`
 * mounts the desktop layer or the mobile one, never both; each layer builds
 * each sidebar once; a section lives on one sidebar; and a workspace lands in
 * one section. A row that is present but not on screen belongs to a sidebar
 * collapsed to its rail, which is `display: none` -- `scrollIntoView` there
 * does nothing, and there is no second copy to try instead.
 *
 * The caller expands the SECTION first when it is collapsed; a row inside a
 * collapsed section has no box to scroll to.
 */
export function revealWorkspaceRow(workspaceId: string): void {
  setWorkspacesExpanded([workspaceId], true)
  document
    .querySelector(`[data-testid="workspace-item-${CSS.escape(workspaceId)}"]`)
    ?.scrollIntoView({ block: 'nearest' })
}

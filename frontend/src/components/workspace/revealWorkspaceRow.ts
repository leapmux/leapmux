import { setWorkspacesExpanded } from './expandedWorkspaces'

/**
 * Bring one workspace row into view, with its tab tree open.
 *
 * A DOM read, deliberately: the row lives inside a scroll container the sidebar
 * owns, several components below whoever asks for the reveal, and threading a
 * ref up through the section list to serve one menu item would put a second
 * handle on every row.
 *
 * EVERY visible copy is scrolled, because the app mounts the sidebar twice --
 * the desktop pane and the mobile overlay -- and only one of them is on screen.
 * `scrollIntoView` on a hidden element does nothing, so scrolling both is
 * correct rather than merely harmless.
 *
 * The caller expands the SECTION first when it is collapsed; a row inside a
 * collapsed section has no box to scroll to.
 */
export function revealWorkspaceRow(workspaceId: string): void {
  setWorkspacesExpanded([workspaceId], true)
  for (const el of document.querySelectorAll(`[data-testid="workspace-item-${CSS.escape(workspaceId)}"]`))
    el.scrollIntoView({ block: 'nearest' })
}

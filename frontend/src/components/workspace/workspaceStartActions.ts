/**
 * Where a new tab opened from a workspace row starts.
 *
 * `workspaceId` is here for the same reason it is on a `BranchRef`: the sidebar
 * lists EVERY workspace's rows, so the row the user clicked often belongs to a
 * workspace they are not looking at, and the tab has to land in that one rather
 * than in whichever is active.
 */
export interface WorkspaceStartAt {
  workspaceId: string
  workerId: string
  workingDir: string
}

/**
 * The two tab-creation actions a workspace row offers.
 *
 * A type of its own rather than a synthetic {@link BranchRef}: that type
 * promises `branchName` and `tabs`, and a fake ref survives exactly until
 * someone reads them. A workspace row knows a repository, not a branch.
 *
 * Both open a DIALOG rather than an inline provider/shell list.
 * `BranchContextMenu` can afford `useAvailableProviders` and
 * `useAvailableShells` because one branch menu is one `workerId`; a workspace
 * spans N repositories on up to N workers, so an inline list would have to fan
 * out per repository on every open.
 */
export interface WorkspaceStartActions {
  /** Open the New agent dialog, pre-filled with this checkout. */
  onNewAgentAt: (at: WorkspaceStartAt) => void
  /** Open the New terminal dialog, pre-filled with this checkout. */
  onNewTerminalAt: (at: WorkspaceStartAt) => void
}

/**
 * Inputs for {@link isShellReady}. Kept as a plain object so the predicate
 * is unit-testable without mounting AppShell.
 */
export interface ShellReadyArgs {
  /** Workspace list load has completed (success or failure). */
  workspacesLoaded: boolean
  /** Section list load has completed (success or failure). */
  sectionsLoaded: boolean
  /** How many workspaces the last successful-shaped list carried. */
  workspaceCount: number
  /** Non-null when the last workspace list attempt failed. */
  workspaceError: string | null
  /** Currently selected workspace id, or null when nothing is selected. */
  activeWorkspaceId: string | null
  /**
   * True when the active workspace has its CRDT record and projected tree —
   * the same gate DesktopLayout uses before painting tiles.
   */
  centerReady: boolean
  /** The CRDT bootstrap watchdog fired without delivering the workspace. */
  bootstrapTimedOut: boolean
}

/**
 * Whether AppShell may lift its BootSplash overlay and show the chrome.
 *
 * AuthGuard clears its own splash as soon as the session restore finishes.
 * The shell then still has to fetch workspaces/sections and wait for the
 * CRDT tree (tabs). Revealing chrome before that paints an empty sidebar and
 * an empty tab bar, then fills them in — the flash this gate exists to stop.
 *
 * Returns false until both lists have completed an attempt. After that:
 * - centerReady: active workspace and its tabs are real
 * - bootstrapTimedOut: do not hang forever on a wedged userevents socket
 * - zero workspaces and no error: the genuine empty state
 * - list failed and nothing is selected: toast already told the user; show
 *   the shell rather than a permanent splash
 *
 * Deliberately NOT ready when workspaces exist but no id is selected yet:
 * that is the gap before `resolveActiveWorkspace` adopts, and treating it as
 * the empty state would flash the create-workspace screen then jump.
 */
export function isShellReady(args: ShellReadyArgs): boolean {
  if (!args.workspacesLoaded || !args.sectionsLoaded)
    return false

  if (args.centerReady || args.bootstrapTimedOut)
    return true

  if (args.workspaceError !== null && args.activeWorkspaceId === null)
    return true

  if (args.workspaceCount === 0 && args.workspaceError === null)
    return true

  // Workspaces exist (or an active id is held) but the centre is not ready
  // and the watchdog has not fired: keep the overlay up.
  return false
}

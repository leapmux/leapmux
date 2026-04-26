/**
 * Single source of truth for the document.title format used across the app.
 *
 * The title shape is `${part} - LeapMux` (or just `LeapMux` when no part is
 * provided). Routes and the active-workspace effect should use these helpers
 * rather than mutating `document.title` directly so the format stays
 * consistent — and so it stays unit-testable without rendering the route.
 */

const SUFFIX = 'LeapMux'

/** Set the document title to "${part} - LeapMux", falling back to bare "LeapMux" if part is empty. */
export function setPageTitle(part: string): void {
  document.title = part ? `${part} - ${SUFFIX}` : SUFFIX
}

/**
 * Title for a workspace route. Falls back to "Untitled" when the workspace
 * has no title set (e.g. while loading or for a freshly-created tab).
 */
export function setWorkspaceTitle(workspaceTitle: string | undefined | null): void {
  setPageTitle(workspaceTitle && workspaceTitle.length > 0 ? workspaceTitle : 'Untitled')
}

/** Title shown when the user is on an org route without an active workspace. */
export function setDashboardTitle(): void {
  setPageTitle('Dashboard')
}

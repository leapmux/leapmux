// Canonical sessionStorage keys for per-workspace tab/focus state.
//
// The persistence write path (useTabPersistence) and the workspace
// restore path (useWorkspaceRestore + AppShell + the sidebar's tab
// preview write) all spell out the same `leapmux:…:${wsId}` template
// strings. A typo in any one of them silently drops persistence in
// that direction. Routing every call through this file makes the
// template literally a function — typo means build-time error.
//
// Keep in lockstep with browserStorage.ts (which owns the localStorage
// keys). sessionStorage and localStorage have different lifetimes, so
// they intentionally live in separate modules.

export const ACTIVE_WORKSPACE_KEY = 'leapmux:activeWorkspace'

// One-shot gate for AppShell's macOS "install leapmux on PATH" prompt:
// the check runs at most once per browser session regardless of how many
// times the user enters/leaves the workspace route.
export const CLI_PATH_CHECKED_KEY = 'leapmux:cli-path-checked'

export function activeTabKey(workspaceId: string): string {
  return `leapmux:activeTab:${workspaceId}`
}

export function tileActiveTabsKey(workspaceId: string): string {
  return `leapmux:tileActiveTabs:${workspaceId}`
}

export function focusedTileKey(workspaceId: string): string {
  return `leapmux:focusedTile:${workspaceId}`
}

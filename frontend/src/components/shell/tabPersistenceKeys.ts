// Canonical templated storage key builders. The persistence write path
// (useTabPersistence), the restore path (restoreTabSelection), the
// workspace switcher, and AppShell itself all need the same
// `leapmux:…:${id}` template. Routing every call
// through these helpers makes the template literally a function — typo
// means build-time error. The prefix constants themselves and the
// non-templated singletons (`KEY_CLI_PATH_CHECKED`,
// `KEY_EXPANDED_WORKSPACES`) live in `~/lib/browserStorage` next to the
// TTL registry that grants them persistence.
//
// All but `activeWorkspaceKey` are per-workspace sessionStorage keys.
// `activeWorkspaceKey` is per-USER and localStorage — it has to outlive
// a tab close, because it is the only record of which workspace to
// reopen once the URL stopped carrying one.

import { PREFIX_ACTIVE_TAB, PREFIX_ACTIVE_WORKSPACE, PREFIX_FOCUSED_TILE, PREFIX_TILE_ACTIVE_TABS } from '~/lib/browserStorage'

export function activeTabKey(workspaceId: string): string {
  return `${PREFIX_ACTIVE_TAB}${workspaceId}`
}

export function tileActiveTabsKey(workspaceId: string): string {
  return `${PREFIX_TILE_ACTIVE_TABS}${workspaceId}`
}

export function focusedTileKey(workspaceId: string): string {
  return `${PREFIX_FOCUSED_TILE}${workspaceId}`
}

/**
 * localStorage key holding the workspace `userId` was last on. Keyed by
 * user so a second account on the same browser gets its own memory
 * rather than inheriting — or overwriting — the first account's.
 */
export function activeWorkspaceKey(userId: string): string {
  return `${PREFIX_ACTIVE_WORKSPACE}${userId}`
}

// Canonical per-workspace sessionStorage key builders. The
// persistence write path (useTabPersistence) and the workspace restore
// path (useWorkspaceRestore + AppShell + the sidebar's tab preview
// write) all need the same `leapmux:…:${wsId}` template. Routing every
// call through these helpers makes the template literally a function —
// typo means build-time error. The prefix constants themselves and the
// non-templated singletons (`KEY_ACTIVE_WORKSPACE`,
// `KEY_CLI_PATH_CHECKED`) live in `~/lib/browserStorage` next to the
// TTL registry that grants them persistence.

import { PREFIX_ACTIVE_TAB, PREFIX_FOCUSED_TILE, PREFIX_TILE_ACTIVE_TABS } from '~/lib/browserStorage'

export function activeTabKey(workspaceId: string): string {
  return `${PREFIX_ACTIVE_TAB}${workspaceId}`
}

export function tileActiveTabsKey(workspaceId: string): string {
  return `${PREFIX_TILE_ACTIVE_TABS}${workspaceId}`
}

export function focusedTileKey(workspaceId: string): string {
  return `${PREFIX_FOCUSED_TILE}${workspaceId}`
}

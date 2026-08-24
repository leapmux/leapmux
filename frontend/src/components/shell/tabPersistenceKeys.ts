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
// Every key here is a per-workspace sessionStorage key. "Which workspace is
// active" is NOT one of them: it has to outlive a tab close, so it lives in
// localStorage under the exact `KEY_ACTIVE_WORKSPACE`. It used to be templated
// by user id and to need a builder of its own here; `browserStorage` now scopes
// every key to the signed-in account, so the user id is no longer part of any
// name a caller writes.

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

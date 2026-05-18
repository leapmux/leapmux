import { KEY_EXPANDED_WORKSPACES, sessionStorageGet } from '~/lib/browserStorage'

/** Reads the persisted expanded-workspace IDs from sessionStorage. */
export function readExpandedWorkspaceIds(): Set<string> {
  const stored = sessionStorageGet<string[]>(KEY_EXPANDED_WORKSPACES)
  return stored ? new Set(stored) : new Set()
}

/** sessionStorage key holding the JSON-encoded set of expanded workspace IDs. */
export const EXPANDED_WORKSPACES_KEY = 'leapmux:expandedWorkspaces'

/** Reads the persisted expanded-workspace IDs from sessionStorage. */
export function readExpandedWorkspaceIds(): Set<string> {
  try {
    const stored = sessionStorage.getItem(EXPANDED_WORKSPACES_KEY)
    return stored ? new Set(JSON.parse(stored) as string[]) : new Set()
  }
  catch {
    return new Set()
  }
}

import type { Workspace } from '~/generated/proto/leapmux/v1/workspace_pb'
import { assertNever } from '~/lib/assertNever'
import { compareNames, compareOptionalValue } from '~/lib/fileSort'

/**
 * The value the workspace list is ordered by.
 *
 * `manual` is the lexorank order the user built by dragging, and it is the
 * default. The other three are derived, so while one of them is active the view
 * order and the model order differ -- which is what makes a same-section drag
 * undefined. See {@link canReorderWithin}.
 */
export type WorkspaceSortKey = 'manual' | 'name' | 'recent' | 'created'

export type WorkspaceSortDirection = 'asc' | 'desc'

export interface WorkspaceSortOrder {
  key: WorkspaceSortKey
  direction: WorkspaceSortDirection
}

export const DEFAULT_WORKSPACE_SORT_ORDER: WorkspaceSortOrder = { key: 'manual', direction: 'asc' }

export const WORKSPACE_SORT_KEYS: readonly WorkspaceSortKey[] = ['manual', 'name', 'recent', 'created']
export const WORKSPACE_SORT_DIRECTIONS: readonly WorkspaceSortDirection[] = ['asc', 'desc']

const SORT_KEY_LABELS: Record<WorkspaceSortKey, string> = {
  manual: 'Manual',
  name: 'Name',
  recent: 'Recently active',
  created: 'Created',
}

/**
 * Direction labels follow the criterion, because "ascending" says nothing
 * useful about a recency counter or a creation time. The same rule the file
 * sort applies, for the same reason.
 */
const DIRECTION_LABELS: Record<WorkspaceSortKey, Record<WorkspaceSortDirection, string>> = {
  // `manual` ignores the direction entirely, but it still needs a pair: the
  // menu draws both radios whatever the key is, and a missing entry would
  // throw on the lookup.
  manual: { asc: 'Ascending', desc: 'Descending' },
  name: { asc: 'A → Z', desc: 'Z → A' },
  recent: { asc: 'Least recent first', desc: 'Most recent first' },
  created: { asc: 'Oldest first', desc: 'Newest first' },
}

export function sortKeyLabel(key: WorkspaceSortKey): string {
  return SORT_KEY_LABELS[key]
}

export function sortDirectionLabel(key: WorkspaceSortKey, direction: WorkspaceSortDirection): string {
  return DIRECTION_LABELS[key][direction]
}

/**
 * Validates a value read back from browser storage.
 *
 * The stored shape is arbitrary JSON that a previous build (or a hand edit) may
 * have written, and an unrecognized key would otherwise reach
 * `DIRECTION_LABELS[key]` -- which throws rather than degrading.
 */
export function parseWorkspaceSortOrder(raw: unknown): WorkspaceSortOrder {
  if (!raw || typeof raw !== 'object')
    return DEFAULT_WORKSPACE_SORT_ORDER
  const { key, direction } = raw as { key?: unknown, direction?: unknown }
  if (!WORKSPACE_SORT_KEYS.includes(key as WorkspaceSortKey)
    || !WORKSPACE_SORT_DIRECTIONS.includes(direction as WorkspaceSortDirection)) {
    return DEFAULT_WORKSPACE_SORT_ORDER
  }
  return { key: key as WorkspaceSortKey, direction: direction as WorkspaceSortDirection }
}

/**
 * How recently a workspace was used: the highest `mru` across its tabs, or
 * undefined for a workspace no tab of which was activated this session.
 *
 * `mru` is a monotonic COUNTER, not a timestamp (see `BaseTab.mru`): it orders
 * and nothing more, it is per session, and a workspace can genuinely have none.
 */
export type WorkspaceRecencyFn = (workspaceId: string) => number | undefined

/**
 * Order one section's workspaces.
 *
 * `manual` returns the input untouched -- that IS the lexorank order the caller
 * already assembled, and re-deriving it here would be a second answer to what
 * the model already says.
 *
 * Every other key falls back to the title, so the order is total and does not
 * shuffle between two renders of the same list. A workspace with no recency
 * sorts LAST under both directions: the pin happens before the direction flip,
 * so "never used this session" never migrates to the top.
 *
 * Never mutates the input. Returns the input ITSELF when the order needs no
 * work, so a downstream memo can still compare by reference; treat the result
 * as read-only either way.
 */
export function sortWorkspaces(
  workspaces: readonly Workspace[],
  order: WorkspaceSortOrder,
  recencyOf: WorkspaceRecencyFn,
): readonly Workspace[] {
  if (order.key === 'manual')
    return workspaces

  const flip = order.direction === 'desc' ? -1 : 1
  const byTitle = (a: Workspace, b: Workspace) => compareNames(a.title || '', b.title || '')

  // Resolved ONCE per workspace, not once per comparison. `recencyOf` scans
  // every tab of a workspace to find its highest activation counter, and a
  // comparator runs O(n log n) times.
  const recency = order.key === 'recent'
    ? new Map(workspaces.map(w => [w.id, recencyOf(w.id)]))
    : undefined

  return [...workspaces].sort((a, b) => {
    switch (order.key) {
      case 'name':
        return byTitle(a, b) * flip
      case 'recent':
        // A counter of 0 is a real value, so only `undefined` is absent.
        return compareOptionalValue(recency!.get(a.id), recency!.get(b.id), flip) ?? byTitle(a, b)
      case 'created':
        // The hub writes every timestamp in one fixed-width layout, so a
        // lexicographic compare is a chronological one and needs no parsing.
        // An EMPTY string means "not reported", so it maps to `undefined`.
        return compareOptionalValue(a.createdAt || undefined, b.createdAt || undefined, flip)
          ?? byTitle(a, b)
      // `manual` returned above, so this case is unreachable -- but listing
      // every key rather than writing a `default` is what makes a NEW key a
      // compile error here.
      case 'manual':
        return 0
      default:
        return assertNever(order.key)
    }
  })
}

/**
 * Keep the workspaces whose title matches `query`, case-insensitively.
 *
 * Returns the input ITSELF for an empty query, for the same reason
 * {@link sortWorkspaces} does. Never mutates it.
 */
export function filterWorkspaces(workspaces: readonly Workspace[], query: string): readonly Workspace[] {
  const needle = query.trim().toLowerCase()
  if (!needle)
    return workspaces
  return workspaces.filter(w => (w.title || '').toLowerCase().includes(needle))
}

/**
 * Whether a workspace may be dragged to a new position WITHIN its section.
 *
 * A workspace drag does three jobs -- reorder within a section, move to another
 * section, and archive by dropping on Archived -- and only the first is
 * undefined when the view order differs from the model order. So this suppresses
 * the between-row drop targets and nothing else: the grip stays, and both
 * cross-section jobs keep working.
 *
 * The sort is GLOBAL, so a non-manual key disables reordering in every section
 * at once. The filter is per section, and an EMPTY query does not disable
 * anything -- the input being open is not the same as the list being narrowed.
 */
export function canReorderWithin(order: WorkspaceSortOrder, filterQuery: string): boolean {
  return order.key === 'manual' && filterQuery.trim() === ''
}

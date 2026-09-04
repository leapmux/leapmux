import type { WorkspaceSortOrder } from '~/lib/workspaceSort'
import { createAccountScopedSignal } from '~/lib/accountScopedSignal'
import { KEY_WORKSPACE_SORT, localStorageGet, localStorageSet } from '~/lib/browserStorage'
import {
  canReorderWithin as canReorderForOrder,
  DEFAULT_WORKSPACE_SORT_ORDER,
  parseWorkspaceSortOrder,
} from '~/lib/workspaceSort'

/**
 * How the sidebar's workspace lists are ordered and narrowed.
 *
 * Module scope for the same reason as `~/components/workspace/expandedWorkspaces`:
 * one sidebar mounts one `WorkspaceSectionContent` per workspace section, and
 * the section HEADER MENU is a further reader that sits outside all of them.
 * Per-instance state would let the menu's radio disagree with the list it
 * claims to order.
 *
 * The two halves differ deliberately:
 *
 *  - The SORT is one global order, persisted per account. One list is one
 *    order, and a per-section sort would be a second answer to "what order is
 *    this in" that the reorder rule below would then have to ask twice.
 *  - The FILTER is per section and NOT persisted. A filter that survived a
 *    reload would hide workspaces the user has forgotten they filtered, which
 *    reads as data loss rather than as a view. It is still account-scoped, so
 *    it does not follow one account's sections into another's sidebar.
 */

const EMPTY_FILTERS: ReadonlyMap<string, string> = new Map()

const sort = createAccountScopedSignal<WorkspaceSortOrder>(DEFAULT_WORKSPACE_SORT_ORDER, {
  read: () => parseWorkspaceSortOrder(localStorageGet(KEY_WORKSPACE_SORT)),
  write: order => localStorageSet(KEY_WORKSPACE_SORT, order),
})

/** `undefined` for a section means the filter input is not shown at all. */
const filters = createAccountScopedSignal<ReadonlyMap<string, string>>(EMPTY_FILTERS)

/** The order every workspace section is drawn in. Reactive. */
export function workspaceSortOrder(): WorkspaceSortOrder {
  return sort.get()
}

export function setWorkspaceSortOrder(order: WorkspaceSortOrder): void {
  sort.set(order)
}

/**
 * What `sectionId`'s filter input holds, or undefined while it is not shown.
 *
 * ONE value for both questions, so "is the input open" and "what is typed"
 * cannot drift: an open-but-empty input is `''`, and a closed one is absent.
 */
export function sectionFilterQuery(sectionId: string): string | undefined {
  return filters.get().get(sectionId)
}

/** Whether `sectionId` shows its filter input. */
export function isSectionFilterShown(sectionId: string): boolean {
  return filters.get().has(sectionId)
}

/** Show or hide `sectionId`'s filter input. Hiding clears the query with it. */
export function toggleSectionFilter(sectionId: string): void {
  filters.set((prev) => {
    const next = new Map(prev)
    if (next.has(sectionId))
      next.delete(sectionId)
    else
      next.set(sectionId, '')
    return next
  })
}

export function setSectionFilterQuery(sectionId: string, query: string): void {
  filters.set((prev) => {
    if (prev.get(sectionId) === query)
      return prev
    const next = new Map(prev)
    next.set(sectionId, query)
    return next
  })
}

/**
 * Whether `sectionId`'s rows may be dragged to a new position within it.
 *
 * The bound form of `~/lib/workspaceSort`'s predicate: the sort is global and
 * the filter is this section's.
 */
export function canReorderWithinSection(sectionId: string): boolean {
  return canReorderForOrder(workspaceSortOrder(), sectionFilterQuery(sectionId) ?? '')
}

/** Drop the order and every filter. FOR TESTS ONLY. */
export function resetWorkspaceListStateForTests(): void {
  sort.reset()
  filters.reset()
}

import type { WorkspaceSortOrder } from '~/lib/workspaceSort'
import { createEffect, createRoot, createSignal } from 'solid-js'
import {
  hasStorageAccount,
  KEY_WORKSPACE_SORT,
  localStorageGet,
  localStorageSet,
  onStorageAccountChange,
} from '~/lib/browserStorage'
import {
  canReorderWithin as canReorderForOrder,
  DEFAULT_WORKSPACE_SORT_ORDER,
  parseWorkspaceSortOrder,
} from '~/lib/workspaceSort'

/**
 * How the sidebar's workspace lists are ordered and narrowed.
 *
 * Module scope for the same reason as `~/components/workspace/expandedWorkspaces`:
 * one sidebar mounts one `WorkspaceSectionContent` per workspace section, the
 * app mounts the sidebar twice, and the section HEADER MENU is a fourth reader
 * that sits outside all of them. Per-instance state would let the menu's radio
 * disagree with the list it claims to order.
 *
 * The two halves differ deliberately:
 *
 *  - The SORT is one global order, persisted per account. One list is one
 *    order, and a per-section sort would be a second answer to "what order is
 *    this in" that the reorder rule below would then have to ask twice.
 *  - The FILTER is per section and NOT persisted. A filter that survived a
 *    reload would hide workspaces the user has forgotten they filtered, which
 *    reads as data loss rather than as a view.
 */

const EMPTY_FILTERS: ReadonlyMap<string, string> = new Map()

/**
 * Whether localStorage was read for the CURRENT account yet.
 *
 * Declared ahead of the `createRoot` below: Solid flushes the root's effects at
 * the end of that call, so the persist effect reads this while the module body
 * is still running.
 */
let seeded = false

const { sortOrder, setSortOrder, filters, setFilters } = createRoot(() => {
  const [order, setOrder] = createSignal<WorkspaceSortOrder>(DEFAULT_WORKSPACE_SORT_ORDER)
  /** `undefined` for a section means the filter input is not shown at all. */
  const [byId, setById] = createSignal<ReadonlyMap<string, string>>(EMPTY_FILTERS)

  createEffect(() => {
    const current = order()
    // Before an account is set, a write throws -- an account-scoped key has no
    // namespace to resolve under -- and writing the default would overwrite a
    // stored preference this module has not read yet.
    if (!seeded)
      return
    localStorageSet(KEY_WORKSPACE_SORT, current)
  })

  return { sortOrder: order, setSortOrder: setOrder, filters: byId, setFilters: setById }
})

function ensureSeeded(): void {
  if (seeded || !hasStorageAccount())
    return
  seeded = true
  setSortOrder(parseWorkspaceSortOrder(localStorageGet(KEY_WORKSPACE_SORT)))
}

// The namespace moved, so the order belongs to the account that left. Drop it
// and let the next access re-read under the new one. The filters go with it:
// they name sections of the account that left.
onStorageAccountChange(() => {
  seeded = false
  setSortOrder(DEFAULT_WORKSPACE_SORT_ORDER)
  setFilters(EMPTY_FILTERS)
})

/** The order every workspace section is drawn in. Reactive. */
export function workspaceSortOrder(): WorkspaceSortOrder {
  ensureSeeded()
  return sortOrder()
}

export function setWorkspaceSortOrder(order: WorkspaceSortOrder): void {
  ensureSeeded()
  setSortOrder(order)
}

/**
 * What `sectionId`'s filter input holds, or undefined while it is not shown.
 *
 * ONE value for both questions, so "is the input open" and "what is typed"
 * cannot drift: an open-but-empty input is `''`, and a closed one is absent.
 */
export function sectionFilterQuery(sectionId: string): string | undefined {
  return filters().get(sectionId)
}

/** Whether `sectionId` shows its filter input. */
export function isSectionFilterShown(sectionId: string): boolean {
  return filters().has(sectionId)
}

/** Show or hide `sectionId`'s filter input. Hiding clears the query with it. */
export function toggleSectionFilter(sectionId: string): void {
  setFilters((prev) => {
    const next = new Map(prev)
    if (next.has(sectionId))
      next.delete(sectionId)
    else
      next.set(sectionId, '')
    return next
  })
}

export function setSectionFilterQuery(sectionId: string, query: string): void {
  setFilters((prev) => {
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
  seeded = false
  setSortOrder(DEFAULT_WORKSPACE_SORT_ORDER)
  setFilters(EMPTY_FILTERS)
}

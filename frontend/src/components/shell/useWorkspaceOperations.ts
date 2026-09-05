import type { Section } from '~/generated/proto/leapmux/v1/section_pb'
import type { Workspace } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { createSectionStore } from '~/stores/section.store'
import type { Tab } from '~/stores/tab.types'

import { createSignal } from 'solid-js'
import { sectionClient, workspaceClient } from '~/api/clients'
import * as workerRpc from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { canReorderWithinSection, sectionFilterQuery, workspaceSortOrder } from '~/components/workspace/workspaceListState'
import { SectionType } from '~/generated/proto/leapmux/v1/section_pb'
import { WorkspaceArchiveState } from '~/generated/proto/leapmux/v1/workspace_pb'
import { appendPosition, mid } from '~/lib/lexorank'
import { cleanName } from '~/lib/validate'
import { filterWorkspaces, sortWorkspaces } from '~/lib/workspaceSort'
import { isWorkspaceSection } from './sectionUtils'

export interface SectionGroup {
  section: Section
  workspaces: Workspace[]
}

export interface UseWorkspaceOperationsProps {
  workspaces: () => Workspace[]
  activeWorkspaceId: () => string | null
  sectionStore: ReturnType<typeof createSectionStore>
  loadSections: () => Promise<void>
  onSelectWorkspace: (id: string) => void
  onRefreshWorkspaces: () => void | Promise<void>
  onDeleteWorkspace: (deletedId: string, nextWorkspaceId: string | null) => void
  onConfirmDelete?: (workspaceId: string) => Promise<boolean>
  onConfirmArchive?: (workspaceId: string) => Promise<boolean>
  /**
   * Confirm emptying the archive, naming the number of workspaces. ONE prompt
   * for the whole operation -- the per-workspace confirm is suppressed, and it
   * would be unusable anyway: `confirmDeleteWsDialog` is a single-slot dialog
   * state, so N concurrent opens drop N-1 resolvers and those awaits never
   * settle.
   */
  onConfirmEmptyArchive?: (count: number) => Promise<boolean>
  /**
   * A workspace's tabs, for the `recent` sort. Optional: a caller with no tab
   * projection (the section-grouping unit tests) leaves every workspace
   * unranked, which sorts them by title instead.
   */
  getTabsForWorkspace?: (workspaceId: string) => Tab[]
  onPostArchiveWorkspace?: (workspaceId: string) => void
}

/** Where a lifecycle move lands: a section, and a lexorank inside it. */
interface MoveTarget {
  sectionId: string
  position: string
}

export function useWorkspaceOperations(props: UseWorkspaceOperationsProps) {
  const store = props.sectionStore

  const [renamingWorkspaceId, setRenamingWorkspaceId] = createSignal<string | null>(null)
  const [renameValue, setRenameValue] = createSignal('')

  // Per-workspace loading state (ref-counted to handle concurrent operations).
  const [loadingCounts, setLoadingCounts] = createSignal<Map<string, number>>(new Map())

  const startWorkspaceLoading = (workspaceId: string): () => void => {
    setLoadingCounts((prev) => {
      const next = new Map(prev)
      next.set(workspaceId, (next.get(workspaceId) ?? 0) + 1)
      return next
    })
    let called = false
    return () => {
      if (!called) {
        called = true
        setLoadingCounts((prev) => {
          const next = new Map(prev)
          const count = (next.get(workspaceId) ?? 1) - 1
          if (count <= 0)
            next.delete(workspaceId)
          else
            next.set(workspaceId, count)
          return next
        })
      }
    }
  }

  const isWorkspaceLoading = (id: string): boolean => {
    return (loadingCounts().get(id) ?? 0) > 0
  }

  // ---------------------------------------------------------------------------
  // Workspace grouping
  // ---------------------------------------------------------------------------

  /**
   * Build section groups for a given set of sections.
   * Each group pairs a section with the workspaces it contains.
   */
  const buildSectionGroups = (sections: Section[]): SectionGroup[] => {
    const groups: SectionGroup[] = []
    const byId = new Map(props.workspaces().map(w => [w.id, w]))
    // A workspace belongs in exactly ONE place in the sidebar. The item table
    // can transiently carry two rows for the same workspace -- a CLI- or
    // cross-worker-created workspace arrives via both the CRDT projection and
    // the lifecycle event before they reconcile -- and rendering per item put
    // two nodes with the SAME `workspace-item-<id>` test id on screen. That is
    // a broken list for the user and a strict-mode violation for any spec that
    // addresses the row by id (142 and 152 both died that way). First
    // occurrence wins, so a workspace mid-move stays where it was instead of
    // appearing twice.
    const placed = new Set<string>()

    const getWorkspacesForSection = (sectionId: string): Workspace[] => {
      // Defense-in-depth tiebreaker on workspaceId: position is a
      // lexorank string with NO uniqueness constraint (two items can
      // legitimately share a position, e.g. each was dragged as the
      // first into a different section and one of those sections was
      // later deleted into this one). The backend SQL ORDER BY now
      // tiebreaks on workspace_id, but Array.sort is stable only when
      // the comparator returns 0 for genuinely-equal items — so on a
      // tie we MUST keep the comparator's verdict deterministic
      // ourselves rather than letting the upstream array order leak
      // through. Without this, a future refactor that swaps the
      // upstream source (e.g. a Map iteration) could reintroduce the
      // shuffle.
      const sectionItems = store.state.items
        .filter(i => i.sectionId === sectionId)
        .sort((a, b) => {
          const cmp = a.position.localeCompare(b.position)
          if (cmp !== 0)
            return cmp
          return a.workspaceId.localeCompare(b.workspaceId)
        })
      const out: Workspace[] = []
      for (const item of sectionItems) {
        if (placed.has(item.workspaceId))
          continue
        const ws = byId.get(item.workspaceId)
        if (!ws)
          continue
        placed.add(item.workspaceId)
        out.push(ws)
      }
      return out
    }

    const assignedIds = new Set(store.state.items.map(i => i.workspaceId))
    const unassigned = props.workspaces().filter(w => !assignedIds.has(w.id))

    for (const section of sections) {
      if (isWorkspaceSection(section.sectionType)) {
        const sectionWorkspaces = getWorkspacesForSection(section.id)
        groups.push({
          section,
          workspaces: section.sectionType === SectionType.WORKSPACES_IN_PROGRESS
            // No `!placed.has(...)` filter: `unassigned` is defined as the
            // workspaces NOT in `assignedIds`, and `placed` only ever receives
            // ids drawn from the same `store.state.items` that built it, so the
            // two sets are disjoint by construction.
            ? [...sectionWorkspaces, ...unassigned]
            : sectionWorkspaces,
        })
      }
      else {
        groups.push({ section, workspaces: [] })
      }
    }

    return groups
  }

  // ---------------------------------------------------------------------------
  // Workspace operations
  // ---------------------------------------------------------------------------

  const getSectionId = (workspaceId: string): string | undefined => {
    return store.getSectionForWorkspace(workspaceId)
  }

  const isWorkspaceArchived = (workspaceId: string): boolean => {
    const archivedSection = store.getArchivedSection()
    if (!archivedSection)
      return false
    return getSectionId(workspaceId) === archivedSection.id
  }

  /**
   * Open the inline rename input for one workspace row.
   *
   * It refuses an ARCHIVED workspace, because the hub does: `RenameWorkspace`
   * answers FailedPrecondition there. Hiding the menu item is not enough -- the
   * row's own double-click reaches this directly, so a user could open the
   * input, type a name, and collect a "Failed to rename workspace" toast for a
   * control the app offered. `WorkspaceTabTree.startEditing` guards the sibling
   * tab operation the same way, for the same reason.
   */
  const startRename = (workspace: Workspace) => {
    if (isWorkspaceArchived(workspace.id))
      return
    setRenamingWorkspaceId(workspace.id)
    setRenameValue(workspace.title || 'Untitled')
  }

  const cancelRename = () => {
    setRenamingWorkspaceId(null)
    setRenameValue('')
  }

  const commitRename = async () => {
    const id = renamingWorkspaceId()
    // Send the CLEANED and CUT title, not the raw one: the hub applies this rule
    // to whatever arrives, so the raw text left the sidebar showing one name
    // while the hub stored another until the next refresh overwrote it. An
    // empty result means nothing survived the clean, which is the same answer
    // an empty input gets.
    //
    // `cleanName`, not `sanitizeName`. The two differ on LENGTH: `sanitizeName`
    // REPORTS an over-limit title in its `error` and still returns the full
    // over-limit value, so reading `.value` alone forwarded a title the hub then
    // refused -- the user got a generic "Failed to rename workspace" for a
    // condition the client had already computed. `cleanName` cuts to the same
    // limit the hub enforces and never refuses, which is what the sibling
    // `renameTab` uses for exactly this reason.
    const title = cleanName(renameValue())
    if (!id || !title) {
      cancelRename()
      return
    }
    // Archived between opening the input and committing it. `startRename`
    // refuses an archived workspace, but the row's grip stays draggable while
    // the input is open, so a user can drag the row onto Archived and the blur
    // then commits into a rename the hub answers with FailedPrecondition.
    if (isWorkspaceArchived(id)) {
      cancelRename()
      return
    }
    const done = startWorkspaceLoading(id)
    try {
      await workspaceClient.renameWorkspace({ workspaceId: id, title })
      props.onRefreshWorkspaces()
    }
    catch (err) {
      showWarnToast('Failed to rename workspace', err)
    }
    finally {
      done()
    }
    cancelRename()
  }

  const archiveWorkspace = async (workspaceId: string, target?: MoveTarget, refresh = true) => {
    const archivedSection = target?.sectionId
      ? store.state.sections.find(section => section.id === target.sectionId)
      : store.getArchivedSection()
    if (!archivedSection)
      return
    if (props.onConfirmArchive) {
      const confirmed = await props.onConfirmArchive(workspaceId)
      if (!confirmed)
        return
    }
    const done = startWorkspaceLoading(workspaceId)
    try {
      await workspaceClient.setWorkspaceArchiveState({
        workspaceId,
        archiveState: WorkspaceArchiveState.ARCHIVED,
        destinationSectionId: target?.sectionId ?? '',
        position: target?.position ?? '',
      })
      // The Hub nudged every Worker that hosts one of this workspace's tabs
      // inside the call above, so those processes are already stopping. Apply
      // the local state now: every surface treats the workspace as read-only
      // while that happens.
      store.moveWorkspace(workspaceId, archivedSection.id, target?.position ?? appendPosition(store.getItemsForSection(archivedSection.id)))
      if (refresh)
        await props.loadSections()
      props.onPostArchiveWorkspace?.(workspaceId)
    }
    catch (err) {
      showWarnToast('Failed to archive workspace', err)
    }
    finally {
      done()
    }
  }

  const unarchiveWorkspace = async (workspaceId: string, target?: MoveTarget, refresh = true) => {
    const inProgressSection = target?.sectionId
      ? store.state.sections.find(section => section.id === target.sectionId)
      : store.getInProgressSection()
    if (!inProgressSection)
      return
    const done = startWorkspaceLoading(workspaceId)
    try {
      await workspaceClient.setWorkspaceArchiveState({
        workspaceId,
        archiveState: WorkspaceArchiveState.ACTIVE,
        destinationSectionId: target?.sectionId ?? '',
        position: target?.position ?? '',
      })
      store.moveWorkspace(workspaceId, inProgressSection.id, target?.position ?? appendPosition(store.getItemsForSection(inProgressSection.id)))
      if (refresh)
        await props.loadSections()
    }
    catch (err) {
      showWarnToast('Failed to unarchive workspace', err)
    }
    finally {
      done()
    }
  }

  /**
   * Move one workspace into `sectionId`, resolving the archive boundary first.
   *
   * Crossing that boundary is a LIFECYCLE change, not a sidebar move, and the
   * hub enforces it: `SectionService.MoveWorkspace` answers FailedPrecondition
   * for a destination on the other side, because stopping or restarting a
   * workspace's processes must not ride on a generic reorder. So this function
   * routes a crossing through archive/unarchive, and runs the plain move below
   * only when both sides agree.
   *
   * The crossing carries its DESTINATION, so it stays one hub transaction. It
   * used to unarchive into In progress and then issue a second move, which
   * left the workspace resting in In progress whenever that second call
   * failed, and needed a "still archived?" branch to tell the two failures
   * apart. `SetWorkspaceArchiveState` refuses a destination on the wrong side,
   * so the parameter cannot become a second way to archive.
   */
  const moveWorkspace = async (workspaceId: string, sectionId: string, dropPosition?: string) => {
    const destination = store.state.sections.find(section => section.id === sectionId)
    const destinationArchived = destination?.sectionType === SectionType.WORKSPACES_ARCHIVED
    // This code COMPARES the two sides, not the destination alone: a reorder
    // inside the archive is a move within one side, and treating it as a
    // crossing would ask the user to confirm archiving a workspace that is
    // already archived.
    if (destinationArchived !== isWorkspaceArchived(workspaceId)) {
      const target: MoveTarget = { sectionId, position: dropPosition ?? appendPosition(store.getItemsForSection(sectionId)) }
      if (destinationArchived)
        await archiveWorkspace(workspaceId, target)
      else
        await unarchiveWorkspace(workspaceId, target)
      return
    }
    const position = dropPosition ?? appendPosition(store.getItemsForSection(sectionId))
    const done = startWorkspaceLoading(workspaceId)
    try {
      await sectionClient.moveWorkspace({ workspaceId, sectionId, position })
      store.moveWorkspace(workspaceId, sectionId, position)
    }
    catch (err) {
      showWarnToast('Failed to move workspace', err)
    }
    finally {
      done()
    }
  }

  const findFirstNonArchivedWorkspaceId = (): string | null => {
    const allSections = store.state.sections
    for (const section of allSections) {
      if (section.sectionType === SectionType.WORKSPACES_ARCHIVED)
        continue
      if (!isWorkspaceSection(section.sectionType))
        continue
      const items = store.getItemsForSection(section.id)
      if (items.length > 0) {
        const ws = props.workspaces().find(w => w.id === items[0].workspaceId)
        if (ws)
          return ws.id
      }
    }
    return null
  }

  /** Re-read the lists a delete invalidated, then hand the caller the next id. */
  const finishDelete = async (workspaceId: string) => {
    await Promise.all([props.onRefreshWorkspaces(), props.loadSections()])
    if (props.onDeleteWorkspace) {
      const nextId = findFirstNonArchivedWorkspaceId()
      props.onDeleteWorkspace(workspaceId, nextId)
    }
  }

  /**
   * Delete one workspace, WITHOUT asking.
   *
   * Split out of `deleteWorkspace` so `emptyArchive` can ask once for the whole
   * set instead of N times. It is not exported: every caller outside this hook
   * goes through `deleteWorkspace` and gets the confirm.
   */
  const performDelete = async (workspaceId: string, refresh = true) => {
    const done = startWorkspaceLoading(workspaceId)
    try {
      // 1. Hub soft-deletes the workspace and answers with each hosting worker
      //    AND the tabs it must tear down, read inside the delete transaction.
      //
      //    This client used to snapshot the tab list itself beforehand, from the
      //    local CRDT projection. That was three bugs: a tab a peer opened
      //    between the read and the delete was missed, the accessor was optional
      //    so a caller that omitted it silently asked every worker to close
      //    nothing, and the projection it read is a strict SUBSET of the owned
      //    tabs -- so a projection-hidden tab was unreachable rather than late.
      const resp = await workspaceClient.deleteWorkspace({ workspaceId })

      // 2. Clean up resources on each worker via E2EE. Each worker gets exactly
      //    its own tabs; tab ids are unique per USER, not per worker, so a tab
      //    id sent to the wrong machine would close a live tab there.
      await Promise.all(
        resp.workerTabs.map(wt =>
          workerRpc.cleanupWorkspace(wt.workerId, { tabs: wt.tabs }).catch(() => {}),
        ),
      )

      // A bulk caller passes `refresh: false` and runs this ONCE for the whole
      // set. Both calls are full-collection RPCs, so a per-item refresh costs
      // 2N round trips to fetch N-1 states nobody sees. The two steps move
      // together because `findFirstNonArchivedWorkspaceId` reads the store the
      // refresh just wrote.
      if (refresh)
        await finishDelete(workspaceId)
    }
    catch (err) {
      showWarnToast('Failed to delete workspace', err)
    }
    finally {
      done()
    }
  }

  /**
   * The workspaces currently in the Archived section, unfiltered and in sidebar
   * order.
   *
   * Read fresh at the start of each bulk operation rather than passed in: the
   * loops below await between iterations, and the caller's snapshot would be
   * one lifecycle event out of date.
   *
   * The section menu shows its two bulk items from this, and both operations
   * act on it -- so the menu can no longer offer an irreversible "Empty
   * archive" for a set the user cannot see, nor hide it while the archive is
   * full.
   */
  const archivedWorkspaceIds = (): string[] => {
    const archivedSection = store.getArchivedSection()
    if (!archivedSection)
      return []
    return store.getItemsForSection(archivedSection.id).map(i => i.workspaceId)
  }

  /**
   * Move every archived workspace back to In progress.
   *
   * SEQUENTIAL, and that is still the whole implementation note, though the
   * reason moved. The local `appendPosition` no longer collides -- it is
   * computed beside the store write now -- but the HUB computes the stored rank
   * inside `SetWorkspaceArchiveState`, reading the destination's tail and
   * writing `After(tail)`. Two of these overlapping at READ COMMITTED read the
   * same tail. `appendPositionAfter` on the hub makes that collision
   * impossible, so this loop is no longer the only thing standing between the
   * user and a sidebar that reshuffles on every refresh; it stays sequential
   * because the order the user sees should be the order they archived in.
   *
   * There is no transaction: each workspace is its own pair of RPCs, so a
   * failure part way through leaves the archive partly emptied. Each failed one
   * raises its own toast, which is the whole safety story.
   *
   * ONE refresh at the end, not one per workspace: a per-item reload costs N
   * round trips to fetch N-1 states nobody sees. `performDelete` takes the same
   * parameter for the same reason.
   */
  const unarchiveAll = async () => {
    const inProgressSection = store.getInProgressSection()
    if (!inProgressSection)
      return
    const ids = archivedWorkspaceIds()
    if (ids.length === 0)
      return
    for (const workspaceId of ids)
      await unarchiveWorkspace(workspaceId, undefined, false)
    await props.loadSections()
  }

  /**
   * Delete every archived workspace, after ONE confirm naming the count.
   *
   * Sequential for the same reason as `unarchiveAll`, and for a second one:
   * suppressing the per-workspace confirm is what makes the operation usable at
   * all, and the dialog state behind it holds one payload, so N concurrent
   * opens would drop N-1 resolvers and hang.
   *
   * Not a transaction either. A failure part way through leaves the archive
   * partly emptied, with one warning toast per failed workspace.
   */
  const emptyArchive = async () => {
    const workspaceIds = archivedWorkspaceIds()
    // Nothing to confirm and nothing to do. `archiveWorkspace` and
    // `unarchiveWorkspace` guard a missing SECTION; nothing guarded an empty
    // item list, and asking "delete 0 workspaces?" is a prompt with no answer
    // worth giving.
    if (workspaceIds.length === 0)
      return
    if (props.onConfirmEmptyArchive) {
      const confirmed = await props.onConfirmEmptyArchive(workspaceIds.length)
      if (!confirmed)
        return
    }
    // The INTERSECTION of what the user confirmed with what is still archived.
    // The confirm is an unbounded await, and any workspace lifecycle event
    // reloads the sections under it -- so a workspace unarchived meanwhile is
    // in the snapshot but must not be deleted. Deleting the fresh list instead
    // would have the mirror fault: a workspace archived during the dialog was
    // never part of the count the user agreed to.
    const stillArchived = new Set(archivedWorkspaceIds())
    const doomed = workspaceIds.filter(id => stillArchived.has(id))
    for (const workspaceId of doomed)
      await performDelete(workspaceId, false)
    if (doomed.length > 0)
      await finishDelete(doomed[doomed.length - 1])
  }

  const deleteWorkspace = async (workspaceId: string) => {
    if (props.onConfirmDelete) {
      const confirmed = await props.onConfirmDelete(workspaceId)
      if (!confirmed)
        return
    }
    await performDelete(workspaceId)
  }

  const canAddToSection = (section: Section): boolean => {
    return section.sectionType !== SectionType.WORKSPACES_ARCHIVED
      && isWorkspaceSection(section.sectionType)
  }

  // ---------------------------------------------------------------------------
  // Workspace DnD helpers
  // ---------------------------------------------------------------------------

  const computeDropPosition = (
    wsId: string,
    targetWsId: string,
    targetSectionId: string,
    direction: 'before' | 'after',
  ): string => {
    const items = store.getItemsForSection(targetSectionId)
      .filter(i => i.workspaceId !== wsId)
    const targetIdx = items.findIndex(i => i.workspaceId === targetWsId)
    if (targetIdx < 0) {
      return appendPosition(items)
    }
    if (direction === 'after') {
      const prevPos = items[targetIdx].position
      const nextPos = targetIdx + 1 < items.length ? items[targetIdx + 1].position : ''
      return mid(prevPos, nextPos)
    }
    const prevPos = targetIdx > 0 ? items[targetIdx - 1].position : ''
    const nextPos = items[targetIdx].position
    return mid(prevPos, nextPos)
  }

  const handleWorkspaceDragEnd = ({ draggable, droppable }: { draggable: any, droppable?: any }) => {
    if (!draggable || !droppable || draggable.id === droppable.id)
      return

    const dragId = String(draggable.id)
    const dropId = String(droppable.id)

    if (!dragId.startsWith('ws-'))
      return

    const wsId = dragId.slice(3)
    const fromSectionId = draggable.data?.sectionId as string

    let targetSectionId: string
    let position: string

    if (dropId.startsWith('ws-')) {
      const targetWsId = dropId.slice(3)
      targetSectionId = droppable.data?.sectionId as string

      if (fromSectionId === targetSectionId) {
        // A same-section reorder has no meaning while the view order differs
        // from the model order, which is what a non-manual sort or a live
        // filter produces. The rows stay sortable so a CROSS-section drop can
        // still resolve a row target; only this case is suppressed.
        if (!canReorderWithinSection(targetSectionId))
          return
        const items = store.getItemsForSection(targetSectionId)
        const dragIdx = items.findIndex(i => i.workspaceId === wsId)
        const dropIdx = items.findIndex(i => i.workspaceId === targetWsId)
        const direction = dragIdx >= 0 && dragIdx < dropIdx ? 'after' : 'before'
        position = computeDropPosition(wsId, targetWsId, targetSectionId, direction)
      }
      else {
        position = computeDropPosition(wsId, targetWsId, targetSectionId, 'before')
      }
    }
    else if (dropId.startsWith('section-')) {
      targetSectionId = dropId.slice(8)
      if (fromSectionId === targetSectionId)
        return
      const items = store.getItemsForSection(targetSectionId)
      position = appendPosition(items)
    }
    else {
      return
    }

    // A drop that CROSSES the archive boundary -- in either direction -- is a
    // lifecycle change, and `moveWorkspace` owns what that means: the archive
    // confirmation on the way in, the unarchive on the way out, and the hub
    // call that a plain `MoveWorkspace` refuses. The drop POSITION goes with
    // it, because the lifecycle RPC takes a destination and a rank, so the
    // workspace lands exactly where the user dropped it; a same-side drop
    // keeps the optimistic path below.
    const resolvedTargetSection = store.state.sections.find(s => s.id === targetSectionId)
    const targetArchived = resolvedTargetSection?.sectionType === SectionType.WORKSPACES_ARCHIVED
    if (targetArchived !== isWorkspaceArchived(wsId)) {
      void moveWorkspace(wsId, targetSectionId, position)
      return
    }

    store.moveWorkspace(wsId, targetSectionId, position)
    const done = startWorkspaceLoading(wsId)
    sectionClient.moveWorkspace({ workspaceId: wsId, sectionId: targetSectionId, position })
      .catch((err) => {
        showWarnToast('Failed to reorder workspace', err)
        props.loadSections()
      })
      .finally(() => done())
  }

  // ---------------------------------------------------------------------------
  // Reactive helpers for content factories
  // ---------------------------------------------------------------------------

  /**
   * How recently a workspace was used: the highest `mru` across its tabs.
   *
   * Undefined when the caller supplied no tab accessor, or when no tab of the
   * workspace was activated this session -- `mru` is a per-session counter, so
   * "none" is a real answer and `sortWorkspaces` pins it last.
   */
  const workspaceRecency = (workspaceId: string): number | undefined => {
    const tabs = props.getTabsForWorkspace?.(workspaceId)
    if (!tabs || tabs.length === 0)
      return undefined
    let best: number | undefined
    for (const tab of tabs) {
      if (tab.mru !== undefined && (best === undefined || tab.mru > best))
        best = tab.mru
    }
    return best
  }

  /**
   * One section's workspaces, in the order the rows are drawn.
   *
   * The ONE place the view order is produced, so every consumer -- the rows,
   * the header menu's Collapse all, the repository list -- sees the same list.
   * `buildSectionGroups` above answers the MODEL order (lexorank); the filter
   * and the sort are applied here, on top of it.
   *
   * It is a plain function, and `useSidebarCore` calls it once per section
   * inside one memo. Calling it per read instead cost a fresh filter pass, a
   * fresh sort and two array copies six to eight times per section on every
   * tick of a global signal.
   */
  const getWorkspacesForGroup = (sectionId: string, groups: SectionGroup[]): readonly Workspace[] => {
    const group = groups.find(g => g.section.id === sectionId)
    if (!group)
      return []
    const filtered = filterWorkspaces(group.workspaces, sectionFilterQuery(sectionId) ?? '')
    return sortWorkspaces(filtered, workspaceSortOrder(), workspaceRecency)
  }

  return {
    // Signals
    renamingWorkspaceId,
    renameValue,

    // Grouping
    buildSectionGroups,

    // Operations
    startRename,
    cancelRename,
    commitRename,
    moveWorkspace,
    archiveWorkspace,
    unarchiveWorkspace,
    getWorkspacesForGroup,
    unarchiveAll,
    emptyArchive,
    archivedWorkspaceIds,
    deleteWorkspace,
    canAddToSection,
    isWorkspaceArchived,
    isWorkspaceLoading,
    onRenameInput: (v: string) => setRenameValue(v),

    // DnD
    computeDropPosition,
    handleWorkspaceDragEnd,
  }
}

export type WorkspaceOperations = ReturnType<typeof useWorkspaceOperations>

import type { Section } from '~/generated/proto/leapmux/v1/section_pb'
import type { Workspace } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { createSectionStore } from '~/stores/section.store'

import { createSignal } from 'solid-js'
import { sectionClient, workspaceClient } from '~/api/clients'
import * as workerRpc from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { SectionType } from '~/generated/proto/leapmux/v1/section_pb'
import { appendPosition, mid } from '~/lib/lexorank'
import { cleanName } from '~/lib/validate'
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
  onPostArchiveWorkspace?: (workspaceId: string) => void
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

  const startRename = (workspace: Workspace) => {
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

  const moveWorkspace = async (workspaceId: string, sectionId: string) => {
    const sectionItems = store.getItemsForSection(sectionId)
    const position = appendPosition(sectionItems)
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

  const archiveWorkspace = async (workspaceId: string) => {
    const archivedSection = store.getArchivedSection()
    if (!archivedSection)
      return
    if (props.onConfirmArchive) {
      const confirmed = await props.onConfirmArchive(workspaceId)
      if (!confirmed)
        return
    }
    await moveWorkspace(workspaceId, archivedSection.id)
    props.onPostArchiveWorkspace?.(workspaceId)
  }

  const unarchiveWorkspace = async (workspaceId: string) => {
    const inProgressSection = store.getInProgressSection()
    if (!inProgressSection)
      return
    await moveWorkspace(workspaceId, inProgressSection.id)
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

  const deleteWorkspace = async (workspaceId: string) => {
    if (props.onConfirmDelete) {
      const confirmed = await props.onConfirmDelete(workspaceId)
      if (!confirmed)
        return
    }
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

      await Promise.all([props.onRefreshWorkspaces(), props.loadSections()])

      if (props.onDeleteWorkspace) {
        const nextId = findFirstNonArchivedWorkspaceId()
        props.onDeleteWorkspace(workspaceId, nextId)
      }
    }
    catch (err) {
      showWarnToast('Failed to delete workspace', err)
    }
    finally {
      done()
    }
  }

  const getSectionId = (workspaceId: string): string | undefined => {
    return store.getSectionForWorkspace(workspaceId)
  }

  const isWorkspaceArchived = (workspaceId: string): boolean => {
    const archivedSection = store.getArchivedSection()
    if (!archivedSection)
      return false
    return getSectionId(workspaceId) === archivedSection.id
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

    // If dragging to the archived section, use the archive flow with confirmation
    const resolvedTargetSection = store.state.sections.find(s => s.id === targetSectionId)
    if (resolvedTargetSection?.sectionType === SectionType.WORKSPACES_ARCHIVED) {
      archiveWorkspace(wsId)
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

  const getWorkspacesForGroup = (sectionId: string, groups: SectionGroup[]): Workspace[] => {
    const group = groups.find(g => g.section.id === sectionId)
    return group?.workspaces ?? []
  }

  return {
    // Signals
    renamingWorkspaceId,
    renameValue,

    // Grouping
    buildSectionGroups,
    getWorkspacesForGroup,

    // Operations
    startRename,
    cancelRename,
    commitRename,
    moveWorkspace,
    archiveWorkspace,
    unarchiveWorkspace,
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

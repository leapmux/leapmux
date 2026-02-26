import type { Section } from '~/generated/leapmux/v1/section_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { createSectionStore } from '~/stores/section.store'

import { createMemo, createSignal } from 'solid-js'
import { sectionClient, workspaceClient } from '~/api/clients'
import { showToast } from '~/components/common/Toast'
import { useAuth } from '~/context/AuthContext'
import { SectionType } from '~/generated/leapmux/v1/section_pb'
import { mid } from '~/lib/lexorank'
import { sanitizeName } from '~/lib/validate'
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
  onNewWorkspace: (sectionId: string | null) => void
  onRefreshWorkspaces: () => void | Promise<void>
  onDeleteWorkspace: (deletedId: string, nextWorkspaceId: string | null) => void
}

export function useWorkspaceOperations(props: UseWorkspaceOperationsProps) {
  const auth = useAuth()
  const store = props.sectionStore

  const [renamingWorkspaceId, setRenamingWorkspaceId] = createSignal<string | null>(null)
  const [renameValue, setRenameValue] = createSignal('')
  const [sharingWorkspaceId, setSharingWorkspaceId] = createSignal<string | null>(null)

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

  const currentUserId = createMemo(() => auth.user()?.id ?? '')

  const ownedWorkspaces = createMemo(() =>
    props.workspaces().filter(w => w.createdBy === currentUserId()),
  )
  const sharedWorkspaces = createMemo(() =>
    props.workspaces().filter(w => w.createdBy !== currentUserId()),
  )

  /**
   * Build section groups for a given set of sections.
   * Each group pairs a section with the workspaces it contains.
   */
  const buildSectionGroups = (sections: Section[]): SectionGroup[] => {
    const groups: SectionGroup[] = []

    const getWorkspacesForSection = (sectionId: string): Workspace[] => {
      const sectionItems = store.state.items
        .filter(i => i.sectionId === sectionId)
        .sort((a, b) => a.position.localeCompare(b.position))
      return sectionItems
        .map(i => ownedWorkspaces().find(w => w.id === i.workspaceId))
        .filter((w): w is Workspace => w != null)
    }

    const assignedIds = new Set(store.state.items.map(i => i.workspaceId))
    const unassigned = ownedWorkspaces().filter(w => !assignedIds.has(w.id))

    for (const section of sections) {
      if (isWorkspaceSection(section.sectionType)) {
        const sectionWorkspaces = getWorkspacesForSection(section.id)
        groups.push({
          section,
          workspaces: section.sectionType === SectionType.WORKSPACES_IN_PROGRESS
            ? [...sectionWorkspaces, ...unassigned]
            : section.sectionType === SectionType.WORKSPACES_SHARED
              ? sharedWorkspaces()
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
    const title = renameValue().trim()
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
      showToast(err instanceof Error ? err.message : 'Failed to rename workspace', 'danger')
    }
    finally {
      done()
    }
    cancelRename()
  }

  const moveWorkspace = async (workspaceId: string, sectionId: string) => {
    const sectionItems = store.getItemsForSection(sectionId)
    const lastItem = sectionItems[sectionItems.length - 1]
    const position = lastItem ? mid(lastItem.position, '') : mid('', '')
    const done = startWorkspaceLoading(workspaceId)
    try {
      await sectionClient.moveWorkspace({ workspaceId, sectionId, position })
      store.moveWorkspace(workspaceId, sectionId, position)
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to move workspace', 'danger')
    }
    finally {
      done()
    }
  }

  const archiveWorkspace = async (workspaceId: string) => {
    const archivedSection = store.getArchivedSection()
    if (!archivedSection)
      return
    await moveWorkspace(workspaceId, archivedSection.id)
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
    // eslint-disable-next-line no-alert
    if (!confirm('Are you sure you want to delete this workspace? This cannot be undone.'))
      return
    const done = startWorkspaceLoading(workspaceId)
    try {
      await workspaceClient.deleteWorkspace({ workspaceId })
      await Promise.all([props.onRefreshWorkspaces(), props.loadSections()])

      if (props.onDeleteWorkspace) {
        const nextId = findFirstNonArchivedWorkspaceId()
        props.onDeleteWorkspace(workspaceId, nextId)
      }
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to delete workspace', 'danger')
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
      && section.sectionType !== SectionType.WORKSPACES_SHARED
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
      const lastItem = items[items.length - 1]
      return lastItem ? mid(lastItem.position, '') : mid('', '')
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

    // Don't allow dragging from the Shared section
    const fromSection = store.state.sections.find(s => s.id === fromSectionId)
    if (fromSection?.sectionType === SectionType.WORKSPACES_SHARED)
      return

    let targetSectionId: string
    let position: string

    if (dropId.startsWith('ws-')) {
      const targetWsId = dropId.slice(3)
      targetSectionId = droppable.data?.sectionId as string
      const targetSection = store.state.sections.find(s => s.id === targetSectionId)
      if (targetSection?.sectionType === SectionType.WORKSPACES_SHARED)
        return

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
      const targetSection = store.state.sections.find(s => s.id === targetSectionId)
      if (targetSection?.sectionType === SectionType.WORKSPACES_SHARED || fromSectionId === targetSectionId)
        return
      const items = store.getItemsForSection(targetSectionId)
      const lastItem = items[items.length - 1]
      position = lastItem ? mid(lastItem.position, '') : mid('', '')
    }
    else {
      return
    }

    store.moveWorkspace(wsId, targetSectionId, position)
    const done = startWorkspaceLoading(wsId)
    sectionClient.moveWorkspace({ workspaceId: wsId, sectionId: targetSectionId, position })
      .catch((err) => {
        showToast(err instanceof Error ? err.message : 'Failed to reorder workspace', 'danger')
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

  const isGroupShared = (sectionId: string, groups: SectionGroup[]): boolean => {
    const group = groups.find(g => g.section.id === sectionId)
    return group?.section.sectionType === SectionType.WORKSPACES_SHARED
  }

  return {
    // Signals
    renamingWorkspaceId,
    renameValue,
    sharingWorkspaceId,
    setSharingWorkspaceId,
    currentUserId,
    sharedWorkspaces,

    // Grouping
    buildSectionGroups,
    getWorkspacesForGroup,
    isGroupShared,

    // Operations
    startRename,
    cancelRename,
    commitRename,
    moveWorkspace,
    archiveWorkspace,
    unarchiveWorkspace,
    deleteWorkspace,
    canAddToSection,
    getSectionId,
    isWorkspaceArchived,
    isWorkspaceLoading,
    onRenameInput: (v: string) => setRenameValue(sanitizeName(v).value),

    // DnD
    computeDropPosition,
    handleWorkspaceDragEnd,
  }
}

export type WorkspaceOperations = ReturnType<typeof useWorkspaceOperations>

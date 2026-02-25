import type { Component } from 'solid-js'
import type { SidebarSectionDef } from './CollapsibleSidebar'
import type { Section } from '~/generated/leapmux/v1/section_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { createSectionStore } from '~/stores/section.store'

import Archive from 'lucide-solid/icons/archive'
import CircleUser from 'lucide-solid/icons/circle-user'
import Folder from 'lucide-solid/icons/folder'
import FolderTree from 'lucide-solid/icons/folder-tree'
import Layers from 'lucide-solid/icons/layers'
import ListChecks from 'lucide-solid/icons/list-checks'
import Plus from 'lucide-solid/icons/plus'
import Users from 'lucide-solid/icons/users'
import { createMemo, createSignal, onCleanup, Show } from 'solid-js'
import { sectionClient, workspaceClient } from '~/api/clients'
import { IconButton } from '~/components/common/IconButton'
import { showToast } from '~/components/common/Toast'
import { dragOverlay as wsDragOverlay } from '~/components/workspace/workspaceList.css'
import { WorkspaceSectionContent } from '~/components/workspace/WorkspaceSectionContent'
import { WorkspaceSharingDialog } from '~/components/workspace/WorkspaceSharingDialog'
import { useAuth } from '~/context/AuthContext'
import { SectionType, Sidebar } from '~/generated/leapmux/v1/section_pb'
import { mid } from '~/lib/lexorank'
import { sanitizeName } from '~/lib/validate'
import { iconSize } from '~/styles/tokens'
import { CollapsibleSidebar } from './CollapsibleSidebar'
import * as csStyles from './CollapsibleSidebar.css'
import { useSectionDrag } from './SectionDragContext'
import { UserMenu } from './UserMenu'

interface LeftSidebarProps {
  workspaces: Workspace[]
  activeWorkspaceId: string | null
  sectionStore: ReturnType<typeof createSectionStore>
  loadSections: () => Promise<void>
  onSelectWorkspace: (id: string) => void
  onNewWorkspace: (sectionId: string | null) => void
  onRefreshWorkspaces: () => void
  onDeleteWorkspace: (deletedId: string, nextWorkspaceId: string | null) => void
  isCollapsed: boolean
  onExpand: () => void
  onCollapse?: () => void
  initialOpenSections?: Record<string, boolean>
  initialSectionSizes?: Record<string, number>
  onSectionStateChange?: (openSections: Record<string, boolean>, sectionSizes: Record<string, number>) => void
}

interface SectionGroup {
  section: Section
  workspaces: Workspace[]
}

export const LeftSidebar: Component<LeftSidebarProps> = (props) => {
  const auth = useAuth()
  const store = props.sectionStore
  const { setExternalDragHandler, setExternalOverlayRenderer } = useSectionDrag()

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
    props.workspaces.filter(w => w.createdBy === currentUserId()),
  )
  const sharedWorkspaces = createMemo(() =>
    props.workspaces.filter(w => w.createdBy !== currentUserId()),
  )

  /** Get sections for the left sidebar, sorted by position. */
  const leftSections = createMemo(() =>
    store.getSectionsForSidebar(Sidebar.LEFT),
  )

  const sectionGroups = createMemo((): SectionGroup[] => {
    const sections = leftSections()
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
        // Non-workspace sections (FILES, TODOS) on the left sidebar get empty groups
        groups.push({ section, workspaces: [] })
      }
    }

    return groups
  })

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
    for (const group of sectionGroups()) {
      if (group.section.sectionType === SectionType.WORKSPACES_ARCHIVED)
        continue
      if (group.workspaces.length > 0)
        return group.workspaces[0].id
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
      props.onRefreshWorkspaces()
      await props.loadSections()

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
  // DnD
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

  const handleDragEnd = ({ draggable, droppable }: { draggable: any, droppable?: any }) => {
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

  // Register workspace DnD handlers with the unified SectionDragProvider.
  // This allows workspace dragging to work through the shared DragDropProvider
  // instead of requiring a separate nested provider (which would shadow section DnD).
  setExternalDragHandler(handleDragEnd)
  setExternalOverlayRenderer((draggable: any) => {
    if (!draggable)
      return <></>
    const id = String(draggable.id)
    if (!id.startsWith('ws-'))
      return <></>
    const wsId = id.slice(3)
    const workspace = props.workspaces.find(w => w.id === wsId)
    return workspace
      ? <div class={wsDragOverlay}>{workspace.title || 'Untitled'}</div>
      : <></>
  })
  onCleanup(() => {
    setExternalDragHandler(null)
    setExternalOverlayRenderer(null)
  })

  // ---------------------------------------------------------------------------
  // Section icon mapping
  // ---------------------------------------------------------------------------

  const getSectionIcon = (section: Section) => {
    switch (section.sectionType) {
      case SectionType.WORKSPACES_IN_PROGRESS:
        return Layers
      case SectionType.WORKSPACES_ARCHIVED:
        return Archive
      case SectionType.WORKSPACES_SHARED:
        return Users
      case SectionType.FILES:
        return FolderTree
      case SectionType.TODOS:
        return ListChecks
      default:
        return Folder
    }
  }

  // ---------------------------------------------------------------------------
  // Reactive helpers for content factories.
  // ---------------------------------------------------------------------------

  const getWorkspacesForGroup = (sectionId: string): Workspace[] => {
    const group = sectionGroups().find(g => g.section.id === sectionId)
    return group?.workspaces ?? []
  }

  const isGroupShared = (sectionId: string): boolean => {
    const group = sectionGroups().find(g => g.section.id === sectionId)
    return group?.section.sectionType === SectionType.WORKSPACES_SHARED
  }

  // ---------------------------------------------------------------------------
  // Build sidebar section definitions
  // ---------------------------------------------------------------------------

  const sidebarSections = (): SidebarSectionDef[] => {
    const groups = sectionGroups()
    const sections: SidebarSectionDef[] = []

    for (const group of groups) {
      const sectionId = group.section.id
      const sectionType = group.section.sectionType
      const isShared = sectionType === SectionType.WORKSPACES_SHARED

      if (isWorkspaceSection(sectionType)) {
        sections.push({
          id: sectionId,
          title: group.section.name,
          railIcon: getSectionIcon(group.section),
          railTitle: group.section.name,
          defaultOpen: sectionType !== SectionType.WORKSPACES_ARCHIVED,
          collapsible: true,
          draggable: true,
          visible: !isShared || sharedWorkspaces().length > 0,
          headerActions: canAddToSection(group.section)
            ? (
                <IconButton
                  icon={Plus}
                  iconSize={iconSize.sm}
                  size="md"
                  title={`New workspace in ${group.section.name}`}
                  data-testid={sectionType === SectionType.WORKSPACES_IN_PROGRESS ? 'sidebar-new-workspace' : undefined}
                  onClick={(e) => {
                    e.stopPropagation()
                    e.preventDefault()
                    props.onNewWorkspace(sectionId)
                  }}
                />
              )
            : undefined,
          testId: `section-header-${sectionTypeTestId(sectionType)}`,
          content: () => (
            <WorkspaceSectionContent
              workspaces={getWorkspacesForGroup(sectionId)}
              sectionId={sectionId}
              activeWorkspaceId={props.activeWorkspaceId}
              currentUserId={currentUserId()}
              isVirtual={isGroupShared(sectionId)}
              sections={store.state.sections}
              onSelect={props.onSelectWorkspace}
              onRename={startRename}
              onMoveTo={moveWorkspace}
              onShare={id => setSharingWorkspaceId(id)}
              onArchive={archiveWorkspace}
              onUnarchive={unarchiveWorkspace}
              onDelete={deleteWorkspace}
              getSectionId={getSectionId}
              isArchived={isWorkspaceArchived}
              renamingWorkspaceId={renamingWorkspaceId()}
              renameValue={renameValue()}
              onRenameInput={v => setRenameValue(sanitizeName(v).value)}
              onRenameCommit={commitRename}
              onRenameCancel={cancelRename}
              isWorkspaceLoading={isWorkspaceLoading}
            />
          ),
        })
      }
      else {
        // Non-workspace sections (FILES, TODOS) rendered on the left sidebar.
        // These are placeholder sections; the actual content is rendered by
        // the unified builder in AppShell when moved here.
        sections.push({
          id: sectionId,
          title: group.section.name,
          railIcon: getSectionIcon(group.section),
          railTitle: group.section.name,
          defaultOpen: true,
          collapsible: true,
          draggable: true,
          testId: `section-header-${sectionTypeTestId(sectionType)}`,
          content: () => <></>,
        })
      }
    }

    // User Menu section (rail-only in collapsed, rendered at bottom in expanded)
    sections.push({
      id: 'user-menu',
      title: 'User',
      railOnly: true,
      railPosition: 'bottom',
      collapsible: false,
      railIcon: CircleUser,
      railTitle: 'User menu',
      railElement: (
        <UserMenu
          trigger={<IconButton icon={CircleUser} iconSize={iconSize.lg} size="lg" title="User menu" data-testid="user-menu-trigger" />}
        />
      ),
      content: () => (
        <UserMenu
          trigger={(
            <span class={csStyles.sidebarTitle} style={{ cursor: 'pointer' }} data-testid="user-menu-trigger">
              {auth.user()?.username ?? '...'}
            </span>
          )}
        />
      ),
    })

    return sections
  }

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  return (
    <>
      <CollapsibleSidebar
        sections={sidebarSections()}
        side="left"
        isCollapsed={props.isCollapsed}
        onExpand={props.onExpand}
        onCollapse={props.onCollapse}
        initialOpenSections={props.initialOpenSections}
        initialSectionSizes={props.initialSectionSizes}
        onStateChange={props.onSectionStateChange}
      />

      <Show when={sharingWorkspaceId()}>
        {workspaceId => (
          <WorkspaceSharingDialog
            workspaceId={workspaceId()}
            onClose={() => setSharingWorkspaceId(null)}
            onSaved={() => {
              setSharingWorkspaceId(null)
              props.onRefreshWorkspaces()
            }}
          />
        )}
      </Show>
    </>
  )
}

/** Whether the section type is a workspace section (can contain workspaces). */
function isWorkspaceSection(sectionType: SectionType): boolean {
  return sectionType === SectionType.WORKSPACES_IN_PROGRESS
    || sectionType === SectionType.WORKSPACES_CUSTOM
    || sectionType === SectionType.WORKSPACES_ARCHIVED
    || sectionType === SectionType.WORKSPACES_SHARED
}

/** Map section type to a test ID slug. */
function sectionTypeTestId(sectionType: SectionType): string {
  switch (sectionType) {
    case SectionType.WORKSPACES_IN_PROGRESS: return 'workspaces_in_progress'
    case SectionType.WORKSPACES_CUSTOM: return 'workspaces_custom'
    case SectionType.WORKSPACES_ARCHIVED: return 'workspaces_archived'
    case SectionType.WORKSPACES_SHARED: return 'workspaces_shared'
    case SectionType.FILES: return 'files'
    case SectionType.TODOS: return 'todos'
    default: return String(sectionType)
  }
}

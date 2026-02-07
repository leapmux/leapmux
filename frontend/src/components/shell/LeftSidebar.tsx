import type { Component } from 'solid-js'
import type { SidebarSectionDef } from './CollapsibleSidebar'
import type { Section } from '~/generated/leapmux/v1/section_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'

import { closestCenter, DragDropProvider, DragDropSensors, DragOverlay } from '@thisbeyond/solid-dnd'
import Archive from 'lucide-solid/icons/archive'
import CircleUser from 'lucide-solid/icons/circle-user'
import Folder from 'lucide-solid/icons/folder'
import Layers from 'lucide-solid/icons/layers'
import Plus from 'lucide-solid/icons/plus'
import Users from 'lucide-solid/icons/users'
import { createEffect, createMemo, createSignal, Show } from 'solid-js'
import { sectionClient, workspaceClient } from '~/api/clients'
import { IconButton } from '~/components/common/IconButton'
import { showToast } from '~/components/common/Toast'
import * as wsStyles from '~/components/workspace/workspaceList.css'
import { WorkspaceSectionContent } from '~/components/workspace/WorkspaceSectionContent'
import { WorkspaceSharingDialog } from '~/components/workspace/WorkspaceSharingDialog'
import { useAuth } from '~/context/AuthContext'
import { useOrg } from '~/context/OrgContext'
import { SectionType } from '~/generated/leapmux/v1/section_pb'
import { mid } from '~/lib/lexorank'
import { createSectionStore } from '~/stores/section.store'
import { iconSize } from '~/styles/tokens'
import { CollapsibleSidebar } from './CollapsibleSidebar'
import * as csStyles from './CollapsibleSidebar.css'
import { UserMenu } from './UserMenu'

interface LeftSidebarProps {
  workspaces: Workspace[]
  activeWorkspaceId: string | null
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
  isVirtual?: boolean
}

export const LeftSidebar: Component<LeftSidebarProps> = (props) => {
  const auth = useAuth()
  const org = useOrg()
  const store = createSectionStore()

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
  // Load sections
  // ---------------------------------------------------------------------------

  const loadSections = async () => {
    const orgId = org.orgId()
    if (!orgId)
      return
    store.setLoading(true)
    try {
      const resp = await sectionClient.listSections({ orgId })
      store.setSections(resp.sections)
      store.setItems(resp.items)
    }
    catch (err) {
      store.setError(err instanceof Error ? err.message : 'Failed to load sections')
    }
    finally {
      store.setLoading(false)
    }
  }

  createEffect(() => {
    if (org.orgId()) {
      loadSections()
    }
  })

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

  const sectionGroups = createMemo((): SectionGroup[] => {
    const sections = store.state.sections
    const groups: SectionGroup[] = []

    const inProgressSection = sections.find(s => s.sectionType === SectionType.IN_PROGRESS)
    const archivedSection = sections.find(s => s.sectionType === SectionType.ARCHIVED)
    const customSections = sections
      .filter(s => s.sectionType === SectionType.CUSTOM)
      .sort((a, b) => a.position.localeCompare(b.position))

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

    if (inProgressSection) {
      groups.push({
        section: inProgressSection,
        workspaces: [...getWorkspacesForSection(inProgressSection.id), ...unassigned],
      })
    }

    for (const section of customSections) {
      groups.push({
        section,
        workspaces: getWorkspacesForSection(section.id),
      })
    }

    const shared = sharedWorkspaces()
    if (shared.length > 0) {
      groups.push({
        section: {
          id: '__shared__',
          name: 'Shared',
          position: '',
          sectionType: SectionType.UNSPECIFIED,
        } as Section,
        workspaces: shared,
        isVirtual: true,
      })
    }

    if (archivedSection) {
      groups.push({
        section: archivedSection,
        workspaces: getWorkspacesForSection(archivedSection.id),
      })
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
      if (group.section.sectionType === SectionType.ARCHIVED)
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
      await loadSections()

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
    return section.sectionType !== SectionType.ARCHIVED && section.id !== '__shared__'
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
    if (fromSectionId === '__shared__')
      return

    let targetSectionId: string
    let position: string

    if (dropId.startsWith('ws-')) {
      const targetWsId = dropId.slice(3)
      targetSectionId = droppable.data?.sectionId as string
      if (targetSectionId === '__shared__')
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
      if (targetSectionId === '__shared__' || fromSectionId === targetSectionId)
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
        loadSections()
      })
      .finally(() => done())
  }

  // ---------------------------------------------------------------------------
  // Section icon mapping
  // ---------------------------------------------------------------------------

  const getSectionIcon = (section: Section, isVirtual?: boolean) => {
    if (isVirtual)
      return Users
    switch (section.sectionType) {
      case SectionType.IN_PROGRESS:
        return Layers
      case SectionType.ARCHIVED:
        return Archive
      default:
        return Folder
    }
  }

  // ---------------------------------------------------------------------------
  // Reactive helpers for content factories.
  //
  // Content factories are called once per section mount (see CollapsibleSidebar
  // ID-based <For>).  SolidJS compiles JSX props as getters, so reading
  // reactive state (sectionGroups(), currentUserId(), etc.) inside the factory
  // keeps the component's props reactive even though the factory itself is
  // called only once.
  // ---------------------------------------------------------------------------

  const getWorkspacesForGroup = (sectionId: string): Workspace[] => {
    const group = sectionGroups().find(g => g.section.id === sectionId)
    return group?.workspaces ?? []
  }

  const isGroupVirtual = (sectionId: string): boolean => {
    return sectionGroups().find(g => g.section.id === sectionId)?.isVirtual ?? false
  }

  // ---------------------------------------------------------------------------
  // Build sidebar section definitions
  // ---------------------------------------------------------------------------

  const sidebarSections = (): SidebarSectionDef[] => {
    const groups = sectionGroups()
    const sections: SidebarSectionDef[] = []

    for (const group of groups) {
      const sectionId = group.section.id

      sections.push({
        id: sectionId,
        title: group.section.name,
        railIcon: getSectionIcon(group.section, group.isVirtual),
        railTitle: group.section.name,
        defaultOpen: group.section.sectionType !== SectionType.ARCHIVED,
        collapsible: true,
        headerActions: canAddToSection(group.section)
          ? (
              <IconButton
                icon={Plus}
                iconSize={iconSize.sm}
                size="md"
                title={`New workspace in ${group.section.name}`}
                data-testid={group.section.sectionType === SectionType.IN_PROGRESS ? 'sidebar-new-workspace' : undefined}
                onClick={(e) => {
                  e.stopPropagation()
                  e.preventDefault()
                  props.onNewWorkspace(sectionId)
                }}
              />
            )
          : undefined,
        testId: `section-header-${group.isVirtual ? 'shared' : SectionType[group.section.sectionType]?.toLowerCase() ?? group.section.sectionType}`,
        content: () => (
          <WorkspaceSectionContent
            workspaces={getWorkspacesForGroup(sectionId)}
            sectionId={sectionId}
            activeWorkspaceId={props.activeWorkspaceId}
            currentUserId={currentUserId()}
            isVirtual={isGroupVirtual(sectionId)}
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
            onRenameInput={setRenameValue}
            onRenameCommit={commitRename}
            onRenameCancel={cancelRename}
            isWorkspaceLoading={isWorkspaceLoading}
          />
        ),
      })
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
      <DragDropProvider onDragEnd={handleDragEnd} collisionDetector={closestCenter}>
        <DragDropSensors />
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
        <DragOverlay>
          {(draggable: any) => {
            if (!draggable)
              return <></>
            const wsId = String(draggable.id).replace('ws-', '')
            const workspace = props.workspaces.find(w => w.id === wsId)
            return workspace
              ? <div class={wsStyles.dragOverlay}>{workspace.title || 'Untitled'}</div>
              : <></>
          }}
        </DragOverlay>
      </DragDropProvider>

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

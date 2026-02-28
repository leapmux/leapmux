import type { Component } from 'solid-js'
import type { SidebarSectionDef } from './CollapsibleSidebar'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { TodoItem } from '~/stores/chat.store'
import type { createSectionStore } from '~/stores/section.store'

import CircleUser from 'lucide-solid/icons/circle-user'
import Plus from 'lucide-solid/icons/plus'
import { createMemo, onCleanup, Show } from 'solid-js'
import { IconButton } from '~/components/common/IconButton'
import { TodoList } from '~/components/todo/TodoList'
import { DirectoryTree } from '~/components/tree/DirectoryTree'
import { emptySection as emptySectionStyle, dragOverlay as wsDragOverlay } from '~/components/workspace/workspaceList.css'
import { WorkspaceSectionContent } from '~/components/workspace/WorkspaceSectionContent'
import { WorkspaceSharingDialog } from '~/components/workspace/WorkspaceSharingDialog'
import { useAuth } from '~/context/AuthContext'
import { SectionType, Sidebar } from '~/generated/leapmux/v1/section_pb'
import { iconSize } from '~/styles/tokens'
import { CollapsibleSidebar } from './CollapsibleSidebar'
import * as csStyles from './CollapsibleSidebar.css'
import { useSectionDrag } from './SectionDragContext'
import { getSectionIcon, isWorkspaceSection, sectionTypeTestId } from './sectionUtils'
import { UserMenu } from './UserMenu'
import { useWorkspaceOperations } from './useWorkspaceOperations'

interface LeftSidebarProps {
  workspaces: Workspace[]
  activeWorkspaceId: string | null
  sectionStore: ReturnType<typeof createSectionStore>
  loadSections: () => Promise<void>
  onSelectWorkspace: (id: string) => void
  onNewWorkspace: (sectionId: string | null) => void
  onRefreshWorkspaces: () => void | Promise<void>
  onDeleteWorkspace: (deletedId: string, nextWorkspaceId: string | null) => void
  onConfirmDelete?: (workspaceId: string) => Promise<boolean>
  onConfirmArchive?: (workspaceId: string) => Promise<boolean>
  onPostArchiveWorkspace?: (workspaceId: string) => void
  isCollapsed: boolean
  onExpand: () => void
  onCollapse?: () => void
  initialOpenSections?: Record<string, boolean>
  initialSectionSizes?: Record<string, number>
  onSectionStateChange?: (openSections: Record<string, boolean>, sectionSizes: Record<string, number>) => void
  // File/todo props for rendering FILES/TODOS sections moved to this sidebar
  workerId: string
  workingDir: string
  homeDir: string
  fileTreePath: string
  onFileSelect: (path: string) => void
  onFileOpen?: (path: string) => void
  onFileMention?: (path: string) => void
  onOpenTerminal?: (dirPath: string) => void
  showTodos: boolean
  activeTodos: TodoItem[]
}

export const LeftSidebar: Component<LeftSidebarProps> = (props) => {
  const auth = useAuth()
  // eslint-disable-next-line solid/reactivity -- stable store reference for component lifetime
  const store = props.sectionStore
  const { setExternalDragHandler, setExternalOverlayRenderer } = useSectionDrag()

  // Captured from CollapsibleSidebar's expandSectionRef callback.
  let expandSection: ((sectionId: string) => void) | undefined

  /* eslint-disable solid/reactivity -- callbacks are stable references */
  const wsOps = useWorkspaceOperations({
    workspaces: () => props.workspaces,
    activeWorkspaceId: () => props.activeWorkspaceId,
    sectionStore: store,
    loadSections: props.loadSections,
    onSelectWorkspace: props.onSelectWorkspace,
    onNewWorkspace: props.onNewWorkspace,
    onRefreshWorkspaces: props.onRefreshWorkspaces,
    onDeleteWorkspace: props.onDeleteWorkspace,
    onConfirmDelete: props.onConfirmDelete,
    onConfirmArchive: props.onConfirmArchive,
    onPostArchiveWorkspace: (workspaceId) => {
      const archivedSection = store.getArchivedSection()
      if (archivedSection && expandSection)
        expandSection(archivedSection.id)
      props.onPostArchiveWorkspace?.(workspaceId)
    },
  })
  /* eslint-enable solid/reactivity */

  // ---------------------------------------------------------------------------
  // Workspace grouping
  // ---------------------------------------------------------------------------

  /** Get sections for the left sidebar, sorted by position. */
  const leftSections = createMemo(() =>
    store.getSectionsForSidebar(Sidebar.LEFT),
  )

  const sectionGroups = createMemo(() =>
    wsOps.buildSectionGroups(leftSections()),
  )

  // ---------------------------------------------------------------------------
  // DnD
  // ---------------------------------------------------------------------------

  // Register workspace DnD handlers with the unified SectionDragProvider.
  // This allows workspace dragging to work through the shared DragDropProvider
  // instead of requiring a separate nested provider (which would shadow section DnD).
  setExternalDragHandler(wsOps.handleWorkspaceDragEnd)
  // eslint-disable-next-line solid/reactivity -- overlay renderer is called from DragOverlay, not a tracked scope
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
  // Reactive helpers for content factories.
  // ---------------------------------------------------------------------------

  const getWorkspacesForGroup = (sectionId: string): Workspace[] =>
    wsOps.getWorkspacesForGroup(sectionId, sectionGroups())

  const isGroupShared = (sectionId: string): boolean =>
    wsOps.isGroupShared(sectionId, sectionGroups())

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
          visible: !isShared || wsOps.sharedWorkspaces().length > 0,
          headerActions: wsOps.canAddToSection(group.section)
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
              currentUserId={wsOps.currentUserId()}
              isVirtual={isGroupShared(sectionId)}
              sections={store.state.sections}
              onSelect={props.onSelectWorkspace}
              onRename={wsOps.startRename}
              onMoveTo={wsOps.moveWorkspace}
              onShare={id => wsOps.setSharingWorkspaceId(id)}
              onArchive={wsOps.archiveWorkspace}
              onUnarchive={wsOps.unarchiveWorkspace}
              onDelete={wsOps.deleteWorkspace}
              isArchived={wsOps.isWorkspaceArchived}
              renamingWorkspaceId={wsOps.renamingWorkspaceId()}
              renameValue={wsOps.renameValue()}
              onRenameInput={wsOps.onRenameInput}
              onRenameCommit={wsOps.commitRename}
              onRenameCancel={wsOps.cancelRename}
              isWorkspaceLoading={wsOps.isWorkspaceLoading}
            />
          ),
        })
      }
      else if (sectionType === SectionType.FILES) {
        // FILES section moved to the left sidebar — render DirectoryTree
        sections.push({
          id: sectionId,
          title: group.section.name,
          railIcon: getSectionIcon(group.section),
          railTitle: group.section.name,
          defaultOpen: true,
          collapsible: true,
          draggable: true,
          testId: `section-header-${sectionTypeTestId(sectionType)}`,
          content: () => (
            <Show
              when={props.workerId}
              fallback={<div class={emptySectionStyle}>No tab selected</div>}
            >
              <DirectoryTree
                workerId={props.workerId}
                showFiles
                selectedPath={props.fileTreePath}
                onSelect={props.onFileSelect}
                onFileOpen={props.onFileOpen}
                onMention={props.onFileMention}
                onOpenTerminal={props.onOpenTerminal}
                rootPath={props.workingDir || '~'}
                homeDir={props.homeDir}
              />
            </Show>
          ),
        })
      }
      else if (sectionType === SectionType.TODOS) {
        // TODOS section moved to the left sidebar — render TodoList
        sections.push({
          id: sectionId,
          title: group.section.name,
          railIcon: getSectionIcon(group.section),
          railTitle: group.section.name,
          visible: props.showTodos,
          draggable: true,
          testId: `section-header-${sectionTypeTestId(sectionType)}`,
          railBadge: () => (
            <span class={csStyles.railBadgeText}>
              {props.activeTodos.filter(t => t.status === 'completed').length}
              /
              {props.activeTodos.length}
            </span>
          ),
          content: () => <TodoList todos={props.activeTodos} />,
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
        expandSectionRef={fn => expandSection = fn}
      />

      <Show when={wsOps.sharingWorkspaceId()}>
        {workspaceId => (
          <WorkspaceSharingDialog
            workspaceId={workspaceId()}
            onClose={() => wsOps.setSharingWorkspaceId(null)}
            onSaved={() => {
              wsOps.setSharingWorkspaceId(null)
              props.onRefreshWorkspaces()
            }}
          />
        )}
      </Show>
    </>
  )
}

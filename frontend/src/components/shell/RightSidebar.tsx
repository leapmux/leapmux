import type { Component } from 'solid-js'
import type { SidebarSectionDef } from './CollapsibleSidebar'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { TodoItem } from '~/stores/chat.store'
import type { createSectionStore } from '~/stores/section.store'

import Plus from 'lucide-solid/icons/plus'
import { createMemo, Show } from 'solid-js'
import { IconButton } from '~/components/common/IconButton'
import { TodoList } from '~/components/todo/TodoList'
import { DirectoryTree } from '~/components/tree/DirectoryTree'
import * as swlStyles from '~/components/workspace/workspaceList.css'
import { WorkspaceSectionContent } from '~/components/workspace/WorkspaceSectionContent'
import { WorkspaceSharingDialog } from '~/components/workspace/WorkspaceSharingDialog'
import { SectionType, Sidebar } from '~/generated/leapmux/v1/section_pb'
import { iconSize } from '~/styles/tokens'
import { CollapsibleSidebar } from './CollapsibleSidebar'
import * as csStyles from './CollapsibleSidebar.css'
import { getSectionIcon, isWorkspaceSection, sectionTypeTestId } from './sectionUtils'
import { useWorkspaceOperations } from './useWorkspaceOperations'

interface RightSidebarProps {
  workspaceId: string
  workerId: string
  workingDir: string
  homeDir: string
  showTodos: boolean
  activeTodos: TodoItem[]
  fileTreePath: string
  onFileSelect: (path: string) => void
  onFileOpen?: (path: string) => void
  onFileMention?: (path: string) => void
  sectionStore: ReturnType<typeof createSectionStore>
  isCollapsed: boolean
  onExpand: () => void
  onCollapse?: () => void
  initialOpenSections?: Record<string, boolean>
  initialSectionSizes?: Record<string, number>
  onSectionStateChange?: (openSections: Record<string, boolean>, sectionSizes: Record<string, number>) => void
  // Workspace props for rendering workspace sections moved to this sidebar
  workspaces: Workspace[]
  activeWorkspaceId: string | null
  loadSections: () => Promise<void>
  onSelectWorkspace: (id: string) => void
  onNewWorkspace: (sectionId: string | null) => void
  onRefreshWorkspaces: () => void | Promise<void>
  onDeleteWorkspace: (deletedId: string, nextWorkspaceId: string | null) => void
  onConfirmDelete?: (workspaceId: string) => Promise<boolean>
  onConfirmArchive?: (workspaceId: string) => Promise<boolean>
  onPostArchiveWorkspace?: (workspaceId: string) => void
}

export const RightSidebar: Component<RightSidebarProps> = (props) => {
  // eslint-disable-next-line solid/reactivity -- stable store reference for component lifetime
  const store = props.sectionStore

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

  /** Get sections assigned to the right sidebar, sorted by position. */
  const rightSections = createMemo(() =>
    store.getSectionsForSidebar(Sidebar.RIGHT),
  )

  const sectionGroups = createMemo(() =>
    wsOps.buildSectionGroups(rightSections()),
  )

  const getWorkspacesForGroup = (sectionId: string): Workspace[] =>
    wsOps.getWorkspacesForGroup(sectionId, sectionGroups())

  const isGroupShared = (sectionId: string): boolean =>
    wsOps.isGroupShared(sectionId, sectionGroups())

  // Build section definitions reactively from the store
  const sections = (): SidebarSectionDef[] => {
    const secs = rightSections()
    return secs.map((section): SidebarSectionDef => {
      const sectionType = section.sectionType

      if (sectionType === SectionType.FILES) {
        return {
          id: section.id,
          title: section.name,
          railIcon: getSectionIcon(section),
          railTitle: section.name,
          collapsible: secs.length > 1,
          draggable: true,
          testId: 'section-header-files',
          content: () => (
            <Show
              when={props.workerId}
              fallback={<div class={swlStyles.emptySection}>No tab selected</div>}
            >
              <DirectoryTree
                workerId={props.workerId}
                showFiles
                selectedPath={props.fileTreePath}
                onSelect={props.onFileSelect}
                onFileOpen={props.onFileOpen}
                onMention={props.onFileMention}
                rootPath={props.workingDir || '~'}
                homeDir={props.homeDir}
              />
            </Show>
          ),
        }
      }

      if (sectionType === SectionType.TODOS) {
        return {
          id: section.id,
          title: section.name,
          railIcon: getSectionIcon(section),
          railTitle: section.name,
          visible: props.showTodos,
          draggable: true,
          testId: 'section-header-todos',
          railBadge: () => (
            <span class={csStyles.railBadgeText}>
              {props.activeTodos.filter(t => t.status === 'completed').length}
              /
              {props.activeTodos.length}
            </span>
          ),
          content: () => <TodoList todos={props.activeTodos} />,
        }
      }

      if (isWorkspaceSection(sectionType)) {
        // Workspace section moved to the right sidebar — render full workspace content
        const isShared = sectionType === SectionType.WORKSPACES_SHARED
        const sectionId = section.id
        return {
          id: sectionId,
          title: section.name,
          railIcon: getSectionIcon(section),
          railTitle: section.name,
          defaultOpen: sectionType !== SectionType.WORKSPACES_ARCHIVED,
          collapsible: true,
          draggable: true,
          visible: !isShared || wsOps.sharedWorkspaces().length > 0,
          headerActions: wsOps.canAddToSection(section)
            ? (
                <IconButton
                  icon={Plus}
                  iconSize={iconSize.sm}
                  size="md"
                  title={`New workspace in ${section.name}`}
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
        }
      }

      // Unknown section type — empty fallback
      return {
        id: section.id,
        title: section.name,
        railIcon: getSectionIcon(section),
        railTitle: section.name,
        collapsible: true,
        draggable: true,
        testId: `section-header-${sectionTypeTestId(sectionType)}`,
        content: () => <></>,
      }
    })
  }

  return (
    <>
      <CollapsibleSidebar
        sections={sections()}
        side="right"
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

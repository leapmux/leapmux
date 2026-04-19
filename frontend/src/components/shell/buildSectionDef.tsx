import type { Accessor } from 'solid-js'
import type { SidebarSectionDef } from './CollapsibleSidebar'
import type { WorkspaceOperations } from './useWorkspaceOperations'
import type { FilesSectionHandle } from '~/components/tree/FilesSection'
import type { Section } from '~/generated/leapmux/v1/section_pb'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { TodoItem } from '~/stores/chat.store'
import type { createGitFileStatusStore, GitFilterTab } from '~/stores/gitFileStatus.store'
import type { createSectionStore } from '~/stores/section.store'
import type { createTabStore, TabItemOps } from '~/stores/tab.store'
import type { ChannelStatus } from '~/stores/workerChannelStatus.store'
import type { WorkspaceStoreRegistryType } from '~/stores/workspaceStoreRegistry'

import Plus from 'lucide-solid/icons/plus'
import { Show } from 'solid-js'
import { IconButton } from '~/components/common/IconButton'
import { TodoList } from '~/components/todo/TodoList'
import { FilesSection, FilesSectionHeaderActions } from '~/components/tree/FilesSection'
import { WorkerSectionContent } from '~/components/workers/WorkerSectionContent'
import { emptySection as emptySectionStyle } from '~/components/workspace/workspaceList.css'
import { WorkspaceSectionContent } from '~/components/workspace/WorkspaceSectionContent'
import { SectionType } from '~/generated/leapmux/v1/section_pb'
import { flavorFromOs } from '~/lib/paths'
import { shortcutHint } from '~/lib/shortcuts/display'
import * as csStyles from './CollapsibleSidebar.css'
import { getSectionIcon, isWorkspaceSection, sectionTypeTestId } from './sectionUtils'

/**
 * All dependencies needed to build a `SidebarSectionDef` for any section type.
 * Both LeftSidebar and RightSidebar populate this from their props.
 */
export interface SectionDefContext {
  // Section store
  sectionStore: ReturnType<typeof createSectionStore>

  // Workspace operations
  wsOps: WorkspaceOperations
  getWorkspacesForGroup: (sectionId: string) => Workspace[]
  isGroupShared: (sectionId: string) => boolean
  activeWorkspaceId: string | null
  onNewWorkspace: (sectionId: string | null) => void
  onSelectWorkspace: (id: string) => void
  tabStore?: ReturnType<typeof createTabStore>
  registry?: WorkspaceStoreRegistryType
  onTabClick?: (type: number, id: string) => void
  tabItemOps?: TabItemOps
  onExpandWorkspace?: (workspaceId: string) => void

  // Files section
  workerId: string
  workingDir: string
  homeDir: string
  fileTreePath: string
  onFileSelect: (path: string) => void
  onFileOpen?: (path: string, openSource?: GitFilterTab) => void
  onFileMention?: (path: string) => void
  onOpenTerminal?: (dirPath: string) => void
  gitStatusStore?: ReturnType<typeof createGitFileStatusStore>
  activeFilePath?: string
  hasActiveFileTab?: boolean
  turnEndTrigger?: number
  filesSectionHandle: Accessor<FilesSectionHandle | undefined>
  setFilesSectionHandle: (handle: FilesSectionHandle | undefined) => void

  // Todos section
  showTodos: boolean
  activeTodos: TodoItem[]

  // Workers section
  workers: Worker[]
  workerInfoFn: (id: string) => WorkerInfo | null
  channelStatusFn: (id: string) => ChannelStatus
  currentUserId: string
  onAddTunnel: (worker: Worker) => void
  onDeregisterWorker: (worker: Worker) => void
}

/**
 * Maps a section to a `SidebarSectionDef`.
 *
 * This is the single source of truth for section-type → content mapping.
 * Both sidebars use this function, so adding or changing a section type
 * only requires updating this one place.
 */
export function buildSectionDef(
  section: Section,
  ctx: SectionDefContext,
): SidebarSectionDef {
  const sectionType = section.sectionType
  const sectionId = section.id

  if (isWorkspaceSection(sectionType)) {
    const isShared = sectionType === SectionType.WORKSPACES_SHARED
    return {
      id: sectionId,
      title: section.name,
      railIcon: getSectionIcon(section),
      railTitle: section.name,
      defaultOpen: sectionType !== SectionType.WORKSPACES_ARCHIVED,
      collapsible: true,
      draggable: true,
      visible: !isShared || ctx.wsOps.sharedWorkspaces().length > 0,
      headerActions: ctx.wsOps.canAddToSection(section)
        ? (
            <IconButton
              icon={Plus}
              iconSize="sm"
              size="md"
              title={shortcutHint(`New workspace in ${section.name}`, 'app.newWorkspaceDialog')}
              data-testid={sectionType === SectionType.WORKSPACES_IN_PROGRESS ? 'sidebar-new-workspace' : undefined}
              onClick={(e) => {
                e.stopPropagation()
                e.preventDefault()
                ctx.onNewWorkspace(sectionId)
              }}
            />
          )
        : undefined,
      testId: `section-header-${sectionTypeTestId(sectionType)}`,
      content: () => (
        <WorkspaceSectionContent
          workspaces={ctx.getWorkspacesForGroup(sectionId)}
          sectionId={sectionId}
          activeWorkspaceId={ctx.activeWorkspaceId}
          currentUserId={ctx.wsOps.currentUserId()}
          isVirtual={ctx.isGroupShared(sectionId)}
          sections={ctx.sectionStore.state.sections}
          onSelect={ctx.onSelectWorkspace}
          onRename={ctx.wsOps.startRename}
          onMoveTo={ctx.wsOps.moveWorkspace}
          onShare={id => ctx.wsOps.setSharingWorkspaceId(id)}
          onArchive={ctx.wsOps.archiveWorkspace}
          onUnarchive={ctx.wsOps.unarchiveWorkspace}
          onDelete={ctx.wsOps.deleteWorkspace}
          isArchived={ctx.wsOps.isWorkspaceArchived}
          renamingWorkspaceId={ctx.wsOps.renamingWorkspaceId()}
          renameValue={ctx.wsOps.renameValue()}
          onRenameInput={ctx.wsOps.onRenameInput}
          onRenameCommit={ctx.wsOps.commitRename}
          onRenameCancel={ctx.wsOps.cancelRename}
          isWorkspaceLoading={ctx.wsOps.isWorkspaceLoading}
          tabs={ctx.tabStore?.state.tabs ?? []}
          activeTabKey={ctx.tabStore?.state.activeTabKey ?? null}
          getTabsForWorkspace={(wsId: string) => ctx.registry?.get(wsId)?.tabs ?? []}
          getActiveTabKeyForWorkspace={(wsId: string) => ctx.registry?.get(wsId)?.activeTabKey ?? null}
          onTabClick={ctx.onTabClick ?? (() => {})}
          tabItemOps={ctx.tabItemOps}
          onExpandWorkspace={ctx.onExpandWorkspace}
        />
      ),
    }
  }

  if (sectionType === SectionType.FILES) {
    return {
      id: sectionId,
      title: section.name,
      railIcon: getSectionIcon(section),
      railTitle: section.name,
      defaultOpen: true,
      collapsible: true,
      draggable: true,
      testId: `section-header-${sectionTypeTestId(sectionType)}`,
      headerActions: (
        <FilesSectionHeaderActions
          onCollapseAll={() => ctx.filesSectionHandle()?.collapseAll()}
          onLocateFile={() => {
            if (ctx.activeFilePath)
              ctx.onFileSelect(ctx.activeFilePath)
          }}
          onRefresh={() => {
            if (ctx.workerId && ctx.workingDir)
              ctx.gitStatusStore?.refresh(ctx.workerId, ctx.workingDir)
            ctx.filesSectionHandle()?.refresh()
          }}
          hasActiveFileTab={ctx.hasActiveFileTab ?? false}
          isFiltered={() => ctx.filesSectionHandle()?.isFiltered() ?? false}
          flatListMode={() => ctx.filesSectionHandle()?.flatListMode() ?? false}
          onToggleFlatList={() => ctx.filesSectionHandle()?.toggleFlatListMode()}
          showHiddenFiles={() => ctx.filesSectionHandle()?.showHiddenFiles() ?? true}
          onToggleShowHidden={() => ctx.filesSectionHandle()?.toggleShowHiddenFiles()}
        />
      ),
      content: () => (
        <Show
          when={ctx.workerId}
          fallback={<div class={emptySectionStyle}>No tab selected</div>}
        >
          <FilesSection
            workerId={ctx.workerId}
            workingDir={ctx.workingDir}
            homeDir={ctx.homeDir}
            flavor={flavorFromOs(ctx.workerInfoFn(ctx.workerId)?.os)}
            fileTreePath={ctx.fileTreePath}
            onFileSelect={ctx.onFileSelect}
            onFileOpen={ctx.onFileOpen}
            onMention={ctx.onFileMention}
            onOpenTerminal={ctx.onOpenTerminal}
            gitStatusStore={ctx.gitStatusStore!}
            activeFilePath={ctx.activeFilePath}
            hasActiveFileTab={ctx.hasActiveFileTab ?? false}
            turnEndTrigger={ctx.turnEndTrigger}
            ref={ctx.setFilesSectionHandle}
          />
        </Show>
      ),
    }
  }

  if (sectionType === SectionType.TODOS) {
    return {
      id: sectionId,
      title: section.name,
      railIcon: getSectionIcon(section),
      railTitle: section.name,
      visible: ctx.showTodos,
      draggable: true,
      testId: `section-header-${sectionTypeTestId(sectionType)}`,
      railBadge: () => (
        <span class={csStyles.railBadgeText}>
          {ctx.activeTodos.filter(t => t.status === 'completed').length}
          /
          {ctx.activeTodos.length}
        </span>
      ),
      content: () => <TodoList todos={ctx.activeTodos} />,
    }
  }

  if (sectionType === SectionType.WORKERS) {
    return {
      id: sectionId,
      title: section.name,
      railIcon: getSectionIcon(section),
      railTitle: section.name,
      defaultOpen: true,
      collapsible: true,
      draggable: true,
      defaultSize: 0.15,
      testId: `section-header-${sectionTypeTestId(sectionType)}`,
      content: () => (
        <WorkerSectionContent
          workers={ctx.workers}
          workerInfo={ctx.workerInfoFn}
          channelStatus={ctx.channelStatusFn}
          currentUserId={ctx.currentUserId}
          onAddTunnel={ctx.onAddTunnel}
          onDeregister={ctx.onDeregisterWorker}
        />
      ),
    }
  }

  // Unknown section type — empty fallback
  return {
    id: sectionId,
    title: section.name,
    railIcon: getSectionIcon(section),
    railTitle: section.name,
    collapsible: true,
    draggable: true,
    testId: `section-header-${sectionTypeTestId(sectionType)}`,
    content: () => <></>,
  }
}

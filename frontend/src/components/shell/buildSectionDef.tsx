import type { Accessor } from 'solid-js'
import type { SidebarSectionDef } from './CollapsibleSidebar'
import type { WorkspaceOperations } from './useWorkspaceOperations'
import type { FilesSectionHandle } from '~/components/tree/FilesSection'
import type { BranchRef } from '~/components/workspace/WorkspaceTabTree'
import type { Section } from '~/generated/leapmux/v1/section_pb'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import type { TodoItem } from '~/stores/chatTodos'
import type { GitFilterTab } from '~/stores/repoGit.store'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { createSectionStore } from '~/stores/section.store'
import type { TabItemOps } from '~/stores/tab.types'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import type { ChannelStatus } from '~/stores/workerChannelStatus.store'

import Plus from 'lucide-solid/icons/plus'
import { Show } from 'solid-js'
import { BackgroundTaskList } from '~/components/backgroundtasks/BackgroundTaskList'
import { IconButton } from '~/components/common/IconButton'
import { TodoList } from '~/components/todo/TodoList'
import { FilesSection, FilesSectionHeaderActions } from '~/components/tree/FilesSection'
import { WorkerSectionContent } from '~/components/workers/WorkerSectionContent'
import { emptySection as emptySectionStyle } from '~/components/workspace/workspaceList.css'
import { WorkspaceSectionContent } from '~/components/workspace/WorkspaceSectionContent'
import { SectionType } from '~/generated/leapmux/v1/section_pb'
import { flavorFromOs } from '~/lib/paths'
import { shortcutHint } from '~/lib/shortcuts/display'
import { isWorkerKnownOnline } from '~/lib/workerLiveness'
import { countActiveBackgroundTasks } from '~/stores/chatBackgroundTasks'
import { todoProgress } from '~/stores/chatTodos'
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
  activeWorkspaceId: string | null
  onNewWorkspace: (sectionId: string | null) => void
  onSelectWorkspace: (id: string) => void
  view?: TabView
  selection?: TabSelectionStore
  onTabClick?: (type: number, id: string) => void
  tabItemOps?: TabItemOps
  /** Tile ids in top-left-first traversal order for `workspaceId`. */
  getTileOrderForWorkspace?: (workspaceId: string) => readonly string[]
  onChangeBranch?: (ref: BranchRef) => void
  onDeleteBranch?: (ref: BranchRef) => void

  // Files section
  workerId: string
  workingDir: string
  homeDir: string
  fileTreePath: string
  onFileSelect: (path: string) => void
  onFileOpen?: (path: string, openSource?: GitFilterTab) => void
  onFileMention?: (path: string) => void
  onOpenTerminal?: (dirPath: string) => void
  gitStatusStore?: ReturnType<typeof createRepoGitStore>
  activeFilePath?: string
  hasActiveFileTab?: boolean
  turnEndTrigger?: number
  /**
   * Whether the active tab's working dir is settled enough to fetch
   * directory listings / git status. Mirrors the same gate used for
   * repoGitStore.refresh in AppShell. See isTabReadyForGitStatus.
   */
  activeTabReady: boolean
  filesSectionHandle: Accessor<FilesSectionHandle | undefined>
  setFilesSectionHandle: (handle: FilesSectionHandle | undefined) => void

  // Todos section
  showTodos: boolean
  activeTodos: TodoItem[]

  // Background tasks section
  showBackgroundTasks: boolean
  activeBackgroundTasks: BackgroundTaskItem[]
  /** The worker could not answer for this root's registry. */
  activeBackgroundTasksFailed: boolean
  onOpenBackgroundTask?: (item: BackgroundTaskItem) => void

  // Workers section
  workers: Worker[]
  workerInfoFn: (id: string) => WorkerInfo | null
  channelStatusFn: (id: string) => ChannelStatus
  onAddTunnel: (worker: Worker) => void
  onDeregisterWorker: (worker: Worker) => void
  onRegisterWorker: () => void
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
    return {
      id: sectionId,
      title: section.name,
      railIcon: getSectionIcon(section),
      railTitle: section.name,
      defaultOpen: sectionType !== SectionType.WORKSPACES_ARCHIVED,
      collapsible: true,
      draggable: true,
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
          sections={ctx.sectionStore.state.sections}
          onSelect={ctx.onSelectWorkspace}
          onRename={ctx.wsOps.startRename}
          onMoveTo={ctx.wsOps.moveWorkspace}
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
          getTabsForWorkspace={(wsId: string) => ctx.view?.forWorkspace(wsId) ?? []}
          getActiveTabKeyForWorkspace={(wsId: string) => ctx.selection?.activeKeyForWorkspace(wsId) ?? null}
          getTileOrderForWorkspace={(wsId: string) => ctx.getTileOrderForWorkspace?.(wsId) ?? []}
          onTabClick={ctx.onTabClick ?? (() => {})}
          tabItemOps={ctx.tabItemOps}
          workerInfoFn={ctx.workerInfoFn}
          isWorkerKnownOnline={workerId => isWorkerKnownOnline(ctx.workers, workerId)}
          onChangeBranch={ctx.onChangeBranch}
          onDeleteBranch={ctx.onDeleteBranch}
          repoGitStore={ctx.gitStatusStore!}
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
          handle={ctx.filesSectionHandle}
          onLocateFile={() => {
            if (ctx.activeFilePath)
              ctx.onFileSelect(ctx.activeFilePath)
          }}
          // Not on the handle: a refresh also re-reads git status, which the
          // section does not own.
          onRefresh={() => {
            if (ctx.workerId && ctx.workingDir)
              ctx.gitStatusStore?.refresh(ctx.workerId, ctx.workingDir)
            ctx.filesSectionHandle()?.refresh()
          }}
          hasActiveFileTab={ctx.hasActiveFileTab ?? false}
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
            enabled={ctx.activeTabReady}
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
      railBadge: () => {
        const { done, total } = todoProgress(ctx.activeTodos)
        return (
          <span class={csStyles.railBadgeText}>
            {done}
            /
            {total}
          </span>
        )
      },
      content: () => <TodoList todos={ctx.activeTodos} />,
    }
  }

  if (sectionType === SectionType.BACKGROUND_TASKS) {
    // Visible whenever the root has ANY rows (past rows keep the section alive
    // -- viewing finished subagents is a first-class use case), and whenever the
    // LOAD FAILED, so a worker that cannot answer says so rather than taking the
    // section off screen. Hidden only when the registry is truly empty. The
    // badge counts active (pending/running) rows.
    const activeCount = countActiveBackgroundTasks(ctx.activeBackgroundTasks)
    return {
      id: sectionId,
      title: section.name,
      railIcon: getSectionIcon(section),
      railTitle: section.name,
      visible: ctx.showBackgroundTasks,
      draggable: true,
      testId: `section-header-${sectionTypeTestId(sectionType)}`,
      railBadge: activeCount > 0
        ? () => <span class={csStyles.railBadgeText}>{activeCount}</span>
        : undefined,
      content: () => (
        <BackgroundTaskList
          variant="sidebar"
          tasks={ctx.activeBackgroundTasks}
          loadFailed={ctx.activeBackgroundTasksFailed}
          onOpenSubagent={ctx.onOpenBackgroundTask}
        />
      ),
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
      headerActions: (
        <IconButton
          icon={Plus}
          iconSize="sm"
          size="md"
          title="Register worker"
          data-testid="sidebar-register-worker"
          onClick={(e) => {
            e.stopPropagation()
            e.preventDefault()
            ctx.onRegisterWorker()
          }}
        />
      ),
      content: () => (
        <WorkerSectionContent
          workers={ctx.workers}
          workerInfo={ctx.workerInfoFn}
          channelStatus={ctx.channelStatusFn}
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

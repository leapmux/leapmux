import type { Accessor } from 'solid-js'
import type { SidebarSectionDef } from './CollapsibleSidebar'
import type { SectionActions } from './sectionUtils'
import type { WorkspaceOperations } from './useWorkspaceOperations'
import type { FilesSectionHandle } from '~/components/tree/FilesSection'
import type { BranchRefActions } from '~/components/workspace/branchActions'
import type { WorkspaceStartActions } from '~/components/workspace/workspaceStartActions'
import type { WorkspaceStartPoint } from '~/components/workspace/workspaceStartPoint'
import type { Section } from '~/generated/proto/leapmux/v1/section_pb'
import type { Worker } from '~/generated/proto/leapmux/v1/worker_pb'
import type { Workspace } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import type { TodoItem } from '~/stores/chatTodos'
import type { createRepoGitStore, GitFilterTab } from '~/stores/repoGit.store'
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
import { setWorkspacesExpanded } from '~/components/workspace/expandedWorkspaces'
import { revealWorkspaceRow } from '~/components/workspace/revealWorkspaceRow'
import { emptySection as emptySectionStyle } from '~/components/workspace/workspaceList.css'
import { isSectionFilterShown, toggleSectionFilter } from '~/components/workspace/workspaceListState'
import { WorkspaceSectionContent } from '~/components/workspace/WorkspaceSectionContent'
import { SectionType } from '~/generated/proto/leapmux/v1/section_pb'
import { flavorFromOs } from '~/lib/paths'
import { isWorkerKnownOnline } from '~/lib/workerLiveness'
import { isLocalWorker } from '~/lib/workerLocality'
import { countActiveBackgroundTasks } from '~/stores/chatBackgroundTasks'
import { todoProgress } from '~/stores/chatTodos'
import { focusedRepoKeyFromTab, gitStatusProbePath } from '~/stores/repoGit'
import * as csStyles from './CollapsibleSidebar.css'
import { getSectionIcon, isWorkspaceSection, sectionTypeTestId } from './sectionUtils'
import { WorkspaceSectionMenu } from './WorkspaceSectionMenu'

/**
 * All dependencies needed to build a `SidebarSectionDef` for any section type.
 * Both LeftSidebar and RightSidebar populate this from their props.
 */
export interface SectionDefContext {
  // Section store
  sectionStore: ReturnType<typeof createSectionStore>

  // Workspace operations
  wsOps: WorkspaceOperations
  getWorkspacesForGroup: (sectionId: string) => readonly Workspace[]
  activeWorkspaceId: string | null
  /** Open the New workspace dialog, into `sectionId` and from `startPoint`. */
  onNewWorkspace: (sectionId: string | null, startPoint: WorkspaceStartPoint) => void
  onSelectWorkspace: (id: string) => void
  /** Expand a collapsed section, so a revealed row has a box to scroll into. */
  expandSection?: (sectionId: string) => void
  sectionActions: SectionActions
  /** Open a new agent / terminal at one of a workspace's checkouts. */
  workspaceStartActions?: WorkspaceStartActions
  view?: TabView
  selection?: TabSelectionStore
  onTabClick?: (type: number, id: string) => void
  tabItemOps?: TabItemOps
  /** Tile ids in top-left-first traversal order for `workspaceId`. */
  getTileOrderForWorkspace?: (workspaceId: string) => readonly string[]
  /** Branch-menu callbacks, unbound. Each branch row binds them to its own ref. */
  branchActions?: BranchRefActions

  // Files section
  workerId: string
  workingDir: string
  gitToplevel: string
  homeDir: string
  fileTreePath: string
  onFileSelect: (path: string) => void
  onFileOpen?: (path: string, openSource?: GitFilterTab) => void
  onFileMention?: (path: string) => void
  onOpenTerminal?: (dirPath: string) => void
  gitStatusStore: ReturnType<typeof createRepoGitStore>
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
  /**
   * Whether the desktop shell runs its own bundled sidecar. Half of the
   * "is this worker local" test; the other half is the worker's own
   * `autoRegistered` flag. See `~/lib/workerLocality`.
   */
  localSolo: boolean
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

  /**
   * The section as the STORE holds it now, not as this call captured it.
   *
   * `headerActions()` and `content()` run once per section MOUNT, so whatever
   * they close over outlives every later `buildSectionDefs()`. The captured
   * object is a store proxy, so a local patch still reaches it -- but
   * `loadSections` writes through `sectionIdentity.stabilize`, which mints a
   * fresh object for any row whose fields changed, and a locally created
   * section never entered that cache at all. The captured proxy then detaches
   * and keeps the values it had, while the header title beside it updates.
   *
   * Only the fields a mounted node reads go through this. `title`, `railTitle`,
   * `defaultOpen` and `testId` stay on `section`, because `buildSectionDefs()`
   * re-runs and `CollapsibleSidebar` re-reads them from the fresh def.
   */
  const liveSection = () => ctx.sectionStore.state.sections.find(s => s.id === sectionId) ?? section

  /**
   * Run `action` with this section open.
   *
   * Header actions render while a section is COLLAPSED, and a collapsed body is
   * `visibility: hidden` inside a zero-height row -- so anything that reveals a
   * node inside it scrolls a box with no height and the user sees nothing. Any
   * header action that points INTO its own section goes through this.
   */
  const revealing = (action: () => void) => () => {
    ctx.expandSection?.(sectionId)
    action()
  }

  if (isWorkspaceSection(sectionType)) {
    // The workspaces this section holds, for every item that acts on the whole
    // section: the repository list, Collapse/Expand all, and Reveal.
    const workspaceIds = () => ctx.getWorkspacesForGroup(sectionId).map(w => w.id)
    return {
      id: sectionId,
      title: section.name,
      railIcon: getSectionIcon(section),
      railTitle: section.name,
      defaultOpen: sectionType !== SectionType.WORKSPACES_ARCHIVED,
      collapsible: true,
      draggable: true,
      // A MENU, not the `+` it replaces. Archived gains a header action it
      // never had: `canAddToSection` refuses it (a workspace created there
      // would be born read-only), so the old `+` was absent and the bulk
      // archive operations and the section CRUD had nowhere to live.
      headerActions: () => (
        <WorkspaceSectionMenu
          section={liveSection()}
          canCreate={ctx.wsOps.canAddToSection(section)}
          getTabs={() => workspaceIds().flatMap(id => ctx.view?.forWorkspace(id) ?? [])}
          getWorkspaceIds={workspaceIds}
          repoGitStore={ctx.gitStatusStore}
          workerInfoFn={ctx.workerInfoFn}
          isWorkerOnline={workerId => isWorkerKnownOnline(ctx.workers, workerId)}
          hasActiveWorkspace={() => {
            const active = ctx.activeWorkspaceId
            return active !== null && workspaceIds().includes(active)
          }}
          onRevealActiveWorkspace={revealing(() => {
            const active = ctx.activeWorkspaceId
            if (active)
              revealWorkspaceRow(active)
          })}
          // Only SHOWING the box expands the section; hiding it must not.
          onToggleFilter={() => {
            if (!isSectionFilterShown(sectionId))
              ctx.expandSection?.(sectionId)
            toggleSectionFilter(sectionId)
          }}
          onCollapseAll={() => setWorkspacesExpanded(workspaceIds(), false)}
          onExpandAll={() => setWorkspacesExpanded(workspaceIds(), true)}
          onNewWorkspace={startPoint => ctx.onNewWorkspace(sectionId, startPoint)}
          hasArchivedWorkspaces={() => ctx.wsOps.archivedWorkspaceIds().length > 0}
          onUnarchiveAll={() => void ctx.wsOps.unarchiveAll()}
          onEmptyArchive={() => void ctx.wsOps.emptyArchive()}
          onNewSection={() => ctx.sectionActions.onNew(liveSection().sidebar)}
          onRenameSection={() => ctx.sectionActions.onRename(liveSection())}
          onDeleteSection={() => ctx.sectionActions.onDelete(liveSection())}
        />
      ),
      testId: `section-header-${sectionTypeTestId(sectionType)}`,
      content: () => (
        <WorkspaceSectionContent
          workspaces={ctx.getWorkspacesForGroup(sectionId)}
          sectionId={sectionId}
          sectionName={liveSection().name}
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
          // Derived here, beside the liveness predicate and exactly the same
          // way: one line off `ctx.workers` and one flag, rather than a prop
          // threaded down the whole sidebar chain.
          isLocalWorkerFn={workerId => isLocalWorker(ctx.workers, workerId, ctx.localSolo)}
          startActions={ctx.workspaceStartActions}
          branchActions={ctx.branchActions}
          repoGitStore={ctx.gitStatusStore}
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
      headerActions: () => (
        <FilesSectionHeaderActions
          handle={ctx.filesSectionHandle}
          onLocateFile={revealing(() => {
            if (ctx.activeFilePath)
              ctx.onFileSelect(ctx.activeFilePath)
          })}
          // Not on the handle: a refresh also re-reads git status, which the
          // section does not own.
          onRefresh={() => {
            const tab = { workerId: ctx.workerId, gitToplevel: ctx.gitToplevel, workingDir: ctx.workingDir }
            const probeCtx = { gitToplevel: ctx.gitToplevel, workingDir: ctx.workingDir }
            const path = gitStatusProbePath(probeCtx)
            const key = focusedRepoKeyFromTab(tab, probeCtx, ctx.gitStatusStore)
            if (ctx.workerId && path)
              void ctx.gitStatusStore.refresh(ctx.workerId, path, { repoKey: key })
            ctx.filesSectionHandle()?.refresh()
          }}
          hasActiveFileTab={ctx.hasActiveFileTab ?? false}
          gitRefreshing={() => ctx.gitStatusStore.loading()}
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
            gitStatusStore={ctx.gitStatusStore}
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
      headerActions: () => (
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

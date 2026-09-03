import type { SectionDefContext } from './buildSectionDef'
import type { SidebarSectionDef } from './CollapsibleSidebar'
import type { FilesSectionHandle } from '~/components/tree/FilesSection'
import type { BranchRefActions } from '~/components/workspace/branchActions'
import type { Sidebar } from '~/generated/proto/leapmux/v1/section_pb'
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

import { createEffect, createMemo, createSignal, onCleanup } from 'solid-js'
import { registerSidebarFileTreeOps } from '~/lib/fileTreeOps'
import { focusedRepoKeyFromTab, gitStatusProbePath } from '~/stores/repoGit'
import { buildSectionDef } from './buildSectionDef'
import { useWorkspaceOperations } from './useWorkspaceOperations'

// ---------------------------------------------------------------------------
// Shared sidebar props
// ---------------------------------------------------------------------------

/**
 * Props shared by both LeftSidebar and RightSidebar.
 * Each sidebar extends this with its own extras.
 */
export interface SidebarCommonProps {
  // Section store
  sectionStore: ReturnType<typeof createSectionStore>

  // Workspace operations
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

  // Display
  isCollapsed: boolean
  onExpand: () => void
  initialOpenSections?: Record<string, boolean>
  initialSectionSizes?: Record<string, number>
  onSectionStateChange?: (openSections: Record<string, boolean>, sectionSizes: Record<string, number>) => void

  // Content
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
  showTodos: boolean
  activeTodos: TodoItem[]
  showBackgroundTasks: boolean
  activeBackgroundTasks: BackgroundTaskItem[]
  /** The worker could not answer for this root's registry. */
  activeBackgroundTasksFailed: boolean
  onOpenBackgroundTask?: (item: BackgroundTaskItem) => void
  /** Signal bumped on agent turn-end; drives directory tree refresh. */
  turnEndTrigger?: number
  /**
   * Whether the active tab's working dir is on disk and safe to query.
   * Forwarded to the Files section so its directory tree and git status
   * fetches don't fire while a worktree-creating agent is still in
   * STARTING. See isTabReadyForGitStatus for the rationale.
   */
  activeTabReady: boolean

  // Tabs
  view?: TabView
  selection?: TabSelectionStore
  onTabClick?: (type: number, id: string) => void
  tabItemOps?: TabItemOps
  /** Tile ids in top-left-first traversal order for `workspaceId`. */
  getTileOrderForWorkspace?: (workspaceId: string) => readonly string[]
  /** Branch-menu callbacks, unbound. Each branch row binds them to its own ref. */
  branchActions?: BranchRefActions

  // Workers
  workers: Worker[]
  workerInfoFn: (id: string) => WorkerInfo | null
  channelStatusFn: (id: string) => ChannelStatus
  onAddTunnel: (worker: Worker) => void
  onDeregisterWorker: (worker: Worker) => void
  /** Open the "register a new worker" dialog from the Workers section header. */
  onRegisterWorker: () => void
}

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

/**
 * Shared setup for both sidebars: workspace operations, section grouping,
 * and section definition context.
 */
export function useSidebarCore(props: SidebarCommonProps, side: Sidebar) {
  const store = props.sectionStore

  // Captured from CollapsibleSidebar's expandSectionRef callback.
  let expandSection: ((sectionId: string) => void) | undefined

  // Handle for the FilesSection imperative API (e.g., collapseAll).
  // Declared as a signal so reactive reads (e.g., isFiltered in header
  // actions) re-evaluate when the handle is assigned after FilesSection mounts.
  const [filesSectionHandle, setFilesSectionHandle] = createSignal<FilesSectionHandle | undefined>()

  createEffect(() => {
    // eslint-disable-next-line solid/reactivity -- the effect re-runs on the handle and unregister tears down the previous registration
    const handle = filesSectionHandle()
    if (!handle)
      return

    const unregister = registerSidebarFileTreeOps({
      refresh: () => {
        const tab = { workerId: props.workerId, gitToplevel: props.gitToplevel, workingDir: props.workingDir }
        const probeCtx = { gitToplevel: props.gitToplevel, workingDir: props.workingDir }
        const path = gitStatusProbePath(probeCtx)
        const key = focusedRepoKeyFromTab(tab, probeCtx, props.gitStatusStore)
        if (props.workerId && path)
          void props.gitStatusStore.refresh(props.workerId, path, { repoKey: key })
        handle.refresh()
      },
      toggleHiddenFiles: () => handle.toggleShowHiddenFiles(),
    })

    onCleanup(unregister)
  })

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

  const sections = createMemo(() =>
    store.getSectionsForSidebar(side),
  )

  const sectionGroups = createMemo(() =>
    wsOps.buildSectionGroups(sections()),
  )

  /**
   * Creates a `SectionDefContext` from the SolidJS component props, using
   * property getters to preserve reactivity through the SolidJS proxy chain.
   */
  const createCtx = (): SectionDefContext => ({
    sectionStore: store,
    wsOps,
    getWorkspacesForGroup: sectionId =>
      wsOps.getWorkspacesForGroup(sectionId, sectionGroups()),
    get activeWorkspaceId() { return props.activeWorkspaceId },
    onNewWorkspace: props.onNewWorkspace,
    onSelectWorkspace: props.onSelectWorkspace,
    get view() { return props.view },
    get selection() { return props.selection },
    get onTabClick() { return props.onTabClick },
    get tabItemOps() { return props.tabItemOps },
    get getTileOrderForWorkspace() { return props.getTileOrderForWorkspace },
    get branchActions() { return props.branchActions },
    get workerId() { return props.workerId },
    get workingDir() { return props.workingDir },
    get gitToplevel() { return props.gitToplevel },
    get homeDir() { return props.homeDir },
    get fileTreePath() { return props.fileTreePath },
    onFileSelect: props.onFileSelect,
    get onFileOpen() { return props.onFileOpen },
    get onFileMention() { return props.onFileMention },
    get onOpenTerminal() { return props.onOpenTerminal },
    get gitStatusStore() { return props.gitStatusStore },
    get activeFilePath() { return props.activeFilePath },
    get hasActiveFileTab() { return props.hasActiveFileTab },
    get turnEndTrigger() { return props.turnEndTrigger },
    get activeTabReady() { return props.activeTabReady },
    filesSectionHandle,
    setFilesSectionHandle,
    get showTodos() { return props.showTodos },
    get activeTodos() { return props.activeTodos },
    get showBackgroundTasks() { return props.showBackgroundTasks },
    get activeBackgroundTasks() { return props.activeBackgroundTasks },
    get activeBackgroundTasksFailed() { return props.activeBackgroundTasksFailed },
    get onOpenBackgroundTask() { return props.onOpenBackgroundTask },
    get workers() { return props.workers },
    workerInfoFn: props.workerInfoFn,
    channelStatusFn: props.channelStatusFn,
    onAddTunnel: props.onAddTunnel,
    onDeregisterWorker: props.onDeregisterWorker,
    onRegisterWorker: props.onRegisterWorker,
  })

  /** Build `SidebarSectionDef[]` from the current section groups. */
  const buildSectionDefs = (): SidebarSectionDef[] => {
    const ctx = createCtx()
    return sectionGroups().map(group =>
      buildSectionDef(group.section, ctx),
    )
  }

  return {
    store,
    wsOps,
    sections,
    sectionGroups,
    buildSectionDefs,
    expandSectionRef: (fn: (sectionId: string) => void) => { expandSection = fn },
  }
}

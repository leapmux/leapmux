import type { Accessor, JSX } from 'solid-js'
import type { TabContext } from './tabContext'
import type { useTerminalOperations } from './useTerminalOperations'
import type { BranchRef } from '~/components/workspace/WorkspaceTabTree'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { TodoItem } from '~/stores/chatTodos'
import type { createGitFileStatusStore, GitFilterTab } from '~/stores/gitFileStatus.store'
import type { createSectionStore } from '~/stores/section.store'
import type { createTabStore } from '~/stores/tab.store'
import type { TabItemOps } from '~/stores/tab.types'
import type { ChannelStatus } from '~/stores/workerChannelStatus.store'
import type { WorkspaceStoreRegistryType } from '~/stores/workspaceStoreRegistry'
import { LeftSidebar } from '~/components/shell/LeftSidebar'
import { RightSidebar } from '~/components/shell/RightSidebar'
import { relativizePath } from '~/lib/paths'
import { formatFileMention } from '~/lib/quoteUtils'
import { insertIntoMruAgentEditor } from '~/stores/editorRef.store'

export interface SidebarElementsOpts {
  workspaces: Workspace[]
  activeWorkspaceId: string | null
  sectionStore: ReturnType<typeof createSectionStore>
  tabStore: ReturnType<typeof createTabStore>
  registry: WorkspaceStoreRegistryType
  loadSections: () => Promise<void>
  onSelectWorkspace: (id: string) => void
  onNewWorkspace: (sectionId: string | null) => void
  onRefreshWorkspaces: () => void
  onDeleteWorkspace: (deletedId: string, nextWorkspaceId: string | null) => void
  onConfirmDelete: (workspaceId: string) => Promise<boolean>
  onConfirmArchive: (workspaceId: string) => Promise<boolean>
  onPostArchiveWorkspace: (workspaceId: string) => void
  getCurrentTabContext: () => TabContext
  getMruAgentContext: () => Pick<TabContext, 'workingDir' | 'homeDir'>
  fileTreePath: string
  onFileSelect: (path: string) => void
  onFileOpen: (path: string, openSource?: GitFilterTab) => void
  isActiveWorkspaceArchived: boolean
  gitStatusStore: ReturnType<typeof createGitFileStatusStore>
  activeFilePath?: string
  hasActiveFileTab: boolean
  showTodos: boolean
  activeTodos: TodoItem[]
  termOps: ReturnType<typeof useTerminalOperations>
  /** Signal bumped on agent turn-end; drives directory tree refresh. */
  turnEndTrigger: number
  /** Whether the active tab's working dir is on disk and safe to query. */
  activeTabReady: boolean
  // Worker section
  workers: Worker[]
  workerInfoFn: (id: string) => WorkerInfo | null
  channelStatusFn: (id: string) => ChannelStatus
  onAddTunnel: (worker: Worker) => void
  onDeregisterWorker: (worker: Worker) => void
  onRegisterWorker: () => void
  onTabClick: (type: number, id: string) => void
  tabItemOps?: TabItemOps
  onExpandWorkspace: (workspaceId: string) => void
  /** Tile ids in top-left-first traversal order for `workspaceId`. */
  getTileOrderForWorkspace: (workspaceId: string) => string[]
  onChangeBranch?: (ref: BranchRef) => void
  onDeleteBranch?: (ref: BranchRef) => void
}

interface SidebarDisplayOpts {
  isCollapsed: Accessor<boolean>
  onExpand: () => void
  saveSidebarState?: () => void
  initialOpenSections?: Record<string, boolean>
  initialSectionSizes?: Record<string, number>
  onStateChange?: (open: Record<string, boolean>, sizes: Record<string, number>) => void
}

// buildCommonSidebarProps builds the prop bag shared by both
// LeftSidebar and RightSidebar. The two components accept the same
// ~30 props (worker/tab context, file-tree handlers, sidebar
// collapse state, workspace store wiring, etc.); collecting them
// here means a new shared prop is added in one place, and the call
// sites JSX-spread the result.
function buildCommonSidebarProps(opts: SidebarElementsOpts, display?: SidebarDisplayOpts) {
  const ctx = opts.getCurrentTabContext()
  const archived = opts.isActiveWorkspaceArchived
  return {
    workspaces: opts.workspaces,
    activeWorkspaceId: opts.activeWorkspaceId,
    sectionStore: opts.sectionStore,
    loadSections: opts.loadSections,
    onSelectWorkspace: opts.onSelectWorkspace,
    onNewWorkspace: opts.onNewWorkspace,
    onRefreshWorkspaces: opts.onRefreshWorkspaces,
    onDeleteWorkspace: opts.onDeleteWorkspace,
    onConfirmDelete: opts.onConfirmDelete,
    onConfirmArchive: opts.onConfirmArchive,
    onPostArchiveWorkspace: opts.onPostArchiveWorkspace,
    isCollapsed: display?.isCollapsed() ?? false,
    onExpand: display?.onExpand ?? (() => {}),
    initialOpenSections: display?.initialOpenSections,
    initialSectionSizes: display?.initialSectionSizes,
    onSectionStateChange: display?.onStateChange,
    workerId: ctx.workerId,
    workingDir: ctx.workingDir,
    homeDir: ctx.homeDir,
    fileTreePath: opts.fileTreePath,
    onFileSelect: opts.onFileSelect,
    onFileOpen: opts.onFileOpen,
    onFileMention: archived
      ? undefined
      : (path: string) => {
          const mru = opts.getMruAgentContext()
          insertIntoMruAgentEditor(opts.tabStore, formatFileMention(relativizePath(path, mru.workingDir, mru.homeDir)), 'inline')
        },
    onOpenTerminal: archived
      ? undefined
      : (dirPath: string) => opts.termOps.handleOpenTerminal(dirPath),
    showTodos: opts.showTodos,
    activeTodos: opts.activeTodos,
    gitStatusStore: opts.gitStatusStore,
    activeFilePath: opts.activeFilePath,
    hasActiveFileTab: opts.hasActiveFileTab,
    turnEndTrigger: opts.turnEndTrigger,
    activeTabReady: opts.activeTabReady,
    workers: opts.workers,
    workerInfoFn: opts.workerInfoFn,
    channelStatusFn: opts.channelStatusFn,
    onAddTunnel: opts.onAddTunnel,
    onDeregisterWorker: opts.onDeregisterWorker,
    onRegisterWorker: opts.onRegisterWorker,
    tabStore: opts.tabStore,
    registry: opts.registry,
    onTabClick: opts.onTabClick,
    tabItemOps: opts.tabItemOps,
    onExpandWorkspace: opts.onExpandWorkspace,
    getTileOrderForWorkspace: opts.getTileOrderForWorkspace,
    onChangeBranch: opts.onChangeBranch,
    onDeleteBranch: opts.onDeleteBranch,
  }
}

export function createLeftSidebarElement(opts: SidebarElementsOpts, display?: SidebarDisplayOpts): JSX.Element {
  return <LeftSidebar {...buildCommonSidebarProps(opts, display)} />
}

export function createRightSidebarElement(opts: SidebarElementsOpts, display?: SidebarDisplayOpts): JSX.Element {
  return <RightSidebar {...buildCommonSidebarProps(opts, display)} />
}

import type { Accessor, JSX } from 'solid-js'
import type { useTerminalOperations } from './useTerminalOperations'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { TodoItem } from '~/stores/chat.store'
import type { createGitFileStatusStore, GitFilterTab } from '~/stores/gitFileStatus.store'
import type { createSectionStore } from '~/stores/section.store'
import type { createTabStore } from '~/stores/tab.store'
import type { ChannelStatus } from '~/stores/workerChannelStatus.store'
import type { WorkspaceStoreRegistryType } from '~/stores/workspaceStoreRegistry'
import { relativizePath } from '~/components/chat/messageUtils'
import { LeftSidebar } from '~/components/shell/LeftSidebar'
import { RightSidebar } from '~/components/shell/RightSidebar'
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
  getCurrentTabContext: () => { workerId: string, workingDir: string, homeDir: string }
  getMruAgentContext: () => { workingDir: string, homeDir: string }
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
  // Worker section
  workers: Worker[]
  workerInfoFn: (id: string) => WorkerInfo | null
  channelStatusFn: (id: string) => ChannelStatus
  onDeregisterWorker: (worker: Worker) => void
  onTabClick: (type: number, id: string) => void
  onExpandWorkspace: (workspaceId: string) => void
}

interface SidebarDisplayOpts {
  isCollapsed: Accessor<boolean>
  onExpand: () => void
  onCollapse?: () => void
  saveSidebarState?: () => void
  initialOpenSections?: Record<string, boolean>
  initialSectionSizes?: Record<string, number>
  onStateChange?: (open: Record<string, boolean>, sizes: Record<string, number>) => void
}

export function createLeftSidebarElement(opts: SidebarElementsOpts, display?: SidebarDisplayOpts): JSX.Element {
  return (
    <LeftSidebar
      workspaces={opts.workspaces}
      activeWorkspaceId={opts.activeWorkspaceId}
      sectionStore={opts.sectionStore}
      loadSections={opts.loadSections}
      onSelectWorkspace={opts.onSelectWorkspace}
      onNewWorkspace={opts.onNewWorkspace}
      onRefreshWorkspaces={opts.onRefreshWorkspaces}
      onDeleteWorkspace={opts.onDeleteWorkspace}
      onConfirmDelete={opts.onConfirmDelete}
      onConfirmArchive={opts.onConfirmArchive}
      onPostArchiveWorkspace={opts.onPostArchiveWorkspace}
      isCollapsed={display?.isCollapsed() ?? false}
      onExpand={display?.onExpand ?? (() => {})}
      onCollapse={display?.onCollapse}
      initialOpenSections={display?.initialOpenSections}
      initialSectionSizes={display?.initialSectionSizes}
      onSectionStateChange={display?.onStateChange}
      workerId={opts.getCurrentTabContext().workerId}
      workingDir={opts.getCurrentTabContext().workingDir}
      homeDir={opts.getCurrentTabContext().homeDir}
      fileTreePath={opts.fileTreePath}
      onFileSelect={opts.onFileSelect}
      onFileOpen={opts.onFileOpen}
      onFileMention={opts.isActiveWorkspaceArchived
        ? undefined
        : (path) => {
            const ctx = opts.getMruAgentContext()
            insertIntoMruAgentEditor(opts.tabStore, formatFileMention(relativizePath(path, ctx.workingDir, ctx.homeDir)), 'inline')
          }}
      onOpenTerminal={opts.isActiveWorkspaceArchived
        ? undefined
        : dirPath => opts.termOps.handleOpenTerminal(dirPath)}
      showTodos={opts.showTodos}
      activeTodos={opts.activeTodos}
      gitStatusStore={opts.gitStatusStore}
      activeFilePath={opts.activeFilePath}
      hasActiveFileTab={opts.hasActiveFileTab}
      turnEndTrigger={opts.turnEndTrigger}
      workers={opts.workers}
      workerInfoFn={opts.workerInfoFn}
      channelStatusFn={opts.channelStatusFn}
      onDeregisterWorker={opts.onDeregisterWorker}
      tabStore={opts.tabStore}
      registry={opts.registry}
      onTabClick={opts.onTabClick}
      onExpandWorkspace={opts.onExpandWorkspace}
    />
  )
}

export function createRightSidebarElement(opts: SidebarElementsOpts, display?: SidebarDisplayOpts): JSX.Element {
  return (
    <RightSidebar
      workspaceId={opts.activeWorkspaceId ?? ''}
      workerId={opts.getCurrentTabContext().workerId}
      workingDir={opts.getCurrentTabContext().workingDir}
      homeDir={opts.getCurrentTabContext().homeDir}
      showTodos={opts.showTodos}
      activeTodos={opts.activeTodos}
      fileTreePath={opts.fileTreePath}
      onFileSelect={opts.onFileSelect}
      onFileOpen={opts.onFileOpen}
      onFileMention={opts.isActiveWorkspaceArchived
        ? undefined
        : (path) => {
            const ctx = opts.getMruAgentContext()
            insertIntoMruAgentEditor(opts.tabStore, formatFileMention(relativizePath(path, ctx.workingDir, ctx.homeDir)), 'inline')
          }}
      onOpenTerminal={opts.isActiveWorkspaceArchived
        ? undefined
        : dirPath => opts.termOps.handleOpenTerminal(dirPath)}
      sectionStore={opts.sectionStore}
      isCollapsed={display?.isCollapsed() ?? false}
      onExpand={display?.onExpand ?? (() => {})}
      onCollapse={display?.onCollapse}
      initialOpenSections={display?.initialOpenSections}
      initialSectionSizes={display?.initialSectionSizes}
      onSectionStateChange={display?.onStateChange}
      workspaces={opts.workspaces}
      activeWorkspaceId={opts.activeWorkspaceId}
      loadSections={opts.loadSections}
      onSelectWorkspace={opts.onSelectWorkspace}
      onNewWorkspace={opts.onNewWorkspace}
      onRefreshWorkspaces={opts.onRefreshWorkspaces}
      onDeleteWorkspace={opts.onDeleteWorkspace}
      onConfirmDelete={opts.onConfirmDelete}
      onConfirmArchive={opts.onConfirmArchive}
      onPostArchiveWorkspace={opts.onPostArchiveWorkspace}
      gitStatusStore={opts.gitStatusStore}
      activeFilePath={opts.activeFilePath}
      hasActiveFileTab={opts.hasActiveFileTab}
      turnEndTrigger={opts.turnEndTrigger}
      tabStore={opts.tabStore}
      registry={opts.registry}
      onTabClick={opts.onTabClick}
      onExpandWorkspace={opts.onExpandWorkspace}
    />
  )
}

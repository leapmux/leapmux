import type { Accessor, JSX } from 'solid-js'
import type { useTerminalOperations } from './useTerminalOperations'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { Todo } from '~/stores/chat.store'
import type { createSectionStore } from '~/stores/section.store'
import type { createTabStore } from '~/stores/tab.store'
import { relativizePath } from '~/components/chat/messageUtils'
import { LeftSidebar } from '~/components/shell/LeftSidebar'
import { RightSidebar } from '~/components/shell/RightSidebar'
import { formatFileMention } from '~/lib/quoteUtils'
import { insertIntoMruAgentEditor } from '~/stores/editorRef.store'

interface SidebarElementsOpts {
  workspaces: Workspace[]
  activeWorkspaceId: string | null | undefined
  sectionStore: ReturnType<typeof createSectionStore>
  tabStore: ReturnType<typeof createTabStore>
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
  onFileOpen: (path: string) => void
  isActiveWorkspaceArchived: boolean
  showTodos: boolean
  activeTodos: Todo[]
  termOps: ReturnType<typeof useTerminalOperations>
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
    />
  )
}

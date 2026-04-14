import type { JSX } from 'solid-js'
import type { SectionDefContext } from './buildSectionDef'
import type { SidebarSectionDef } from './CollapsibleSidebar'
import type { FilesSectionHandle } from '~/components/tree/FilesSection'
import type { Sidebar } from '~/generated/leapmux/v1/section_pb'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { TodoItem } from '~/stores/chat.store'
import type { createGitFileStatusStore, GitFilterTab } from '~/stores/gitFileStatus.store'
import type { createSectionStore } from '~/stores/section.store'
import type { createTabStore, TabItemOps } from '~/stores/tab.store'
import type { ChannelStatus } from '~/stores/workerChannelStatus.store'
import type { WorkspaceStoreRegistryType } from '~/stores/workspaceStoreRegistry'

import { createEffect, createMemo, createSignal, onCleanup, Show } from 'solid-js'
import { WorkspaceSharingDialog } from '~/components/workspace/WorkspaceSharingDialog'
import { registerSidebarFileTreeOps } from '~/lib/fileTreeOps'
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
  onCollapse?: () => void
  initialOpenSections?: Record<string, boolean>
  initialSectionSizes?: Record<string, number>
  onSectionStateChange?: (openSections: Record<string, boolean>, sectionSizes: Record<string, number>) => void

  // Content
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
  showTodos: boolean
  activeTodos: TodoItem[]
  /** Signal bumped on agent turn-end; drives directory tree refresh. */
  turnEndTrigger?: number

  // Tabs
  tabStore?: ReturnType<typeof createTabStore>
  registry?: WorkspaceStoreRegistryType
  onTabClick?: (type: number, id: string) => void
  tabItemOps?: TabItemOps
  onExpandWorkspace?: (workspaceId: string) => void

  // Workers
  workers: Worker[]
  workerInfoFn: (id: string) => WorkerInfo | null
  channelStatusFn: (id: string) => ChannelStatus
  currentUserId: string
  onAddTunnel: (worker: Worker) => void
  onDeregisterWorker: (worker: Worker) => void
}

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

/**
 * Shared setup for both sidebars: workspace operations, section grouping,
 * section definition context, and the sharing dialog.
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
    const handle = filesSectionHandle()
    if (!handle)
      return

    const unregister = registerSidebarFileTreeOps({
      refresh: () => {
        if (props.workerId && props.workingDir)
          props.gitStatusStore?.refresh(props.workerId, props.workingDir)
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
    isGroupShared: sectionId =>
      wsOps.isGroupShared(sectionId, sectionGroups()),
    get activeWorkspaceId() { return props.activeWorkspaceId },
    onNewWorkspace: props.onNewWorkspace,
    onSelectWorkspace: props.onSelectWorkspace,
    get tabStore() { return props.tabStore },
    get registry() { return props.registry },
    get onTabClick() { return props.onTabClick },
    get tabItemOps() { return props.tabItemOps },
    get onExpandWorkspace() { return props.onExpandWorkspace },
    get workerId() { return props.workerId },
    get workingDir() { return props.workingDir },
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
    filesSectionHandle,
    setFilesSectionHandle,
    get showTodos() { return props.showTodos },
    get activeTodos() { return props.activeTodos },
    get workers() { return props.workers },
    workerInfoFn: props.workerInfoFn,
    channelStatusFn: props.channelStatusFn,
    currentUserId: props.currentUserId,
    onAddTunnel: props.onAddTunnel,
    onDeregisterWorker: props.onDeregisterWorker,
  })

  /** Build `SidebarSectionDef[]` from the current section groups. */
  const buildSectionDefs = (): SidebarSectionDef[] => {
    const ctx = createCtx()
    return sectionGroups().map(group =>
      buildSectionDef(group.section, ctx),
    )
  }

  /** Render the workspace sharing dialog (used by both sidebars). */
  const renderSharingDialog = (): JSX.Element => (
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
  )

  return {
    store,
    wsOps,
    sections,
    sectionGroups,
    buildSectionDefs,
    renderSharingDialog,
    expandSectionRef: (fn: (sectionId: string) => void) => { expandSection = fn },
  }
}

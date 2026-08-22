import type { Accessor, JSX } from 'solid-js'
import type { mruAgentEditorDeps } from './mruAgentEditorDeps'
import type { TabContext } from './tabContext'
import type { useTerminalOperations } from './useTerminalOperations'
import type { BranchRef } from '~/components/workspace/WorkspaceTabTree'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import type { TodoItem } from '~/stores/chatTodos'
import type { createRepoGitStore, GitFilterTab } from '~/stores/repoGit.store'
import type { createSectionStore } from '~/stores/section.store'
import type { TabItemOps } from '~/stores/tab.types'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import type { ChannelStatus } from '~/stores/workerChannelStatus.store'
import { mergeProps } from 'solid-js'
import { LeftSidebar } from '~/components/shell/LeftSidebar'
import { RightSidebar } from '~/components/shell/RightSidebar'
import { relativizePath } from '~/lib/paths'
import { formatFileMention } from '~/lib/quoteUtils'
import { insertIntoMruAgentEditor } from '~/stores/editorRef.store'

export interface SidebarElementsOpts {
  /**
   * Wiring for "insert this text into the MRU agent's editor", built once by
   * AppShell and shared with the tile renderer. See `mruAgentEditorDeps`.
   */
  mruEditorDeps: ReturnType<typeof mruAgentEditorDeps>
  workspaces: Workspace[]
  activeWorkspaceId: string | null
  sectionStore: ReturnType<typeof createSectionStore>
  view: TabView
  selection: TabSelectionStore
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
  gitStatusStore: ReturnType<typeof createRepoGitStore>
  activeFilePath?: string
  hasActiveFileTab: boolean
  showTodos: boolean
  activeTodos: TodoItem[]
  showBackgroundTasks: boolean
  activeBackgroundTasks: BackgroundTaskItem[]
  /** The worker could not answer for this root's registry. */
  activeBackgroundTasksFailed: boolean
  onOpenBackgroundTask?: (item: BackgroundTaskItem) => void
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
  /** Tile ids in top-left-first traversal order for `workspaceId`. */
  getTileOrderForWorkspace: (workspaceId: string) => readonly string[]
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
//
// EVERY reactive value is a getter, for the same reason `sidebarOpts` in
// AppShell makes them getters one hop up: reading them eagerly here undid that.
// A plain read happens in whatever reactive scope calls this, so the sidebar
// was re-CREATED -- not updated -- on every change to the focused tab's
// context, the todo list, a turn ending, a worker list refresh. A remount tears
// down live DOM, which is why an open tree/branch context menu would vanish
// mid-click: the element the user was clicking got detached under them.
//
// Solid's JSX spread accesses props lazily, so the getters keep the components
// mounted and let each one re-render only the part that actually changed.
export function buildCommonSidebarProps(opts: SidebarElementsOpts, display?: SidebarDisplayOpts) {
  // Hoisted so the getters below hand back a STABLE reference. Building the
  // closure inside the getter would mint a new function on every read, and a
  // consumer that keys off the prop -- `<Show when={props.onOpenTerminal}>`,
  // any memo over it -- would see a change that never happened.
  const fileMention = (path: string) => {
    const mru = opts.getMruAgentContext()
    insertIntoMruAgentEditor(opts.mruEditorDeps, formatFileMention(relativizePath(path, mru.workingDir, mru.homeDir)), 'inline')
  }
  const openTerminal = (dirPath: string) => opts.termOps.handleOpenTerminal(dirPath)
  // mergeProps, not a hand-written forward list.
  //
  // Every pass-through prop -- 34 of them -- is forwarded lazily by the
  // mechanism rather than by 34 individually-correct `get` keywords, so a
  // reactive field added to SidebarElementsOpts cannot reintroduce the
  // whole-sidebar remount by being copied eagerly here, and a new pass-through
  // needs no edit in this function at all. mergeProps copies an accessor
  // descriptor AS an accessor for plain-object sources, which is what
  // `sidebarOpts()` returns, so nothing below is invoked at build time.
  //
  // Only the DERIVED entries are written out: the display block (defaulted or
  // renamed), the three fields read off the current tab context, and the two
  // handlers gated on the archived state. Later sources win, so these shadow
  // `opts` where the names would collide -- none do today.
  //
  // The trade: five shell-internal fields (mruEditorDeps, getCurrentTabContext,
  // getMruAgentContext, termOps, isActiveWorkspaceArchived) ride along on the
  // object. Sidebar code cannot reach them -- both sidebars are typed
  // SidebarCommonProps, which does not declare them -- so this is runtime
  // surface, not API surface.
  // Captured rather than returned inline so `solid/reactivity` can see what the
  // merged object is and check its use; returning the call directly leaves the
  // rule unable to analyse it.
  const commonProps = mergeProps(opts, {
    get isCollapsed() { return display?.isCollapsed() ?? false },
    onExpand: display?.onExpand ?? (() => {}),
    initialOpenSections: display?.initialOpenSections,
    initialSectionSizes: display?.initialSectionSizes,
    onSectionStateChange: display?.onStateChange,
    get workerId() { return opts.getCurrentTabContext().workerId },
    get workingDir() { return opts.getCurrentTabContext().workingDir },
    get gitToplevel() { return opts.getCurrentTabContext().gitToplevel },
    get homeDir() { return opts.getCurrentTabContext().homeDir },
    // Archived workspaces expose no mention/terminal affordance, and whether a
    // workspace is archived can change while the sidebar is mounted -- so the
    // undefined-vs-handler choice has to be re-read too, not frozen at build.
    get onFileMention() {
      return opts.isActiveWorkspaceArchived ? undefined : fileMention
    },
    get onOpenTerminal() {
      return opts.isActiveWorkspaceArchived ? undefined : openTerminal
    },
  })
  return commonProps
}

export function createLeftSidebarElement(opts: SidebarElementsOpts, display?: SidebarDisplayOpts): JSX.Element {
  return <LeftSidebar {...buildCommonSidebarProps(opts, display)} />
}

export function createRightSidebarElement(opts: SidebarElementsOpts, display?: SidebarDisplayOpts): JSX.Element {
  return <RightSidebar {...buildCommonSidebarProps(opts, display)} />
}

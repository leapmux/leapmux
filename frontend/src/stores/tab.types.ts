import type { AgentGitStatus, AgentProvider, AgentStatus, AvailableModel, AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import type { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'

/**
 * Module note: tab types live here so the store, op emitters, helpers,
 * and consumers can all import them without pulling in the store
 * factory (which transitively imports SolidJS reactivity primitives).
 * Importing only the types keeps the bundle dependency graph shallow.
 */

export type FileViewMode = 'working' | 'head' | 'staged' | 'unified-diff' | 'split-diff'
export type FileDiffBase = 'head-vs-working' | 'head-vs-staged'
export type FileOpenSource = 'all' | 'changed' | 'staged' | 'unstaged'

/**
 * Fields every tab carries regardless of kind. AGENT/TERMINAL/FILE
 * variants extend BaseTab with their own kind-specific fields and
 * narrow `type` to a literal so consumers can `switch (tab.type)` or
 * use the `isAgentTab` / `isTerminalTab` / `isFileTab` guards below.
 */
export interface BaseTab {
  id: string
  title?: string
  hasNotification?: boolean
  position?: string
  tileId?: string
  workerId?: string
  /**
   * Local-only monotonic activation counter. Higher = more recently
   * activated. Set when the tab is added with `activate: true` or
   * touched via setActiveTab / setActiveTabForTile. Used to derive
   * global + per-tile MRU order without parallel registers.
   *
   * Not persisted in the CRDT and not part of the rendered-tab proto;
   * snapshots / restore round-trip it within the same client session.
   */
  mru?: number
  workingDir?: string
  createdAt?: string
  // ---- Git status (populated for AGENT & TERMINAL tabs; FILE tabs
  // inherit their working-tree's status indirectly via the file path). ----
  gitBranch?: string
  gitOriginUrl?: string
  /**
   * Absolute working-tree root of the tab's enclosing git repository
   * (from `git rev-parse --show-toplevel`). Used to group origin-less
   * repos ("local" repos) in the sidebar tree; the same toplevel means
   * the same repo, different toplevels mean different repos.
   */
  gitToplevel?: string
  gitDiffAdded?: number
  gitDiffDeleted?: number
  gitDiffUntracked?: number
}

/**
 * AGENT tab. Populated on hydration (`protoToAgentTabFields`) and
 * refreshed by the `WatchAgentEvents` `statusChange` handler so the
 * tab is the single source of truth for every per-agent reader.
 */
export interface AgentTab extends BaseTab {
  type: TabType.AGENT
  agentProvider?: AgentProvider
  agentStatus?: AgentStatus
  agentSessionId?: string
  model?: string
  effort?: string
  permissionMode?: string
  extraSettings?: Record<string, string>
  availableModels?: AvailableModel[]
  availableOptionGroups?: AvailableOptionGroup[]
  /**
   * Structured git status reported by the agent process. Flat
   * `gitBranch`/`gitOriginUrl`/`gitToplevel` mirror the most-used fields
   * for sidebar grouping, terminal tabs, and the AppShell git sync;
   * this full record is kept for consumers that need ahead/behind/
   * conflicted/modified/etc.
   */
  agentGitStatus?: AgentGitStatus
  /**
   * Error string carried while AgentStatus.STARTUP_FAILED so the chat
   * startup banner can render the agent's failure reason.
   */
  startupError?: string
  /** Phase label carried while AgentStatus.STARTING (e.g. "Starting Claude…"). */
  startupMessage?: string
}

/** TERMINAL tab. Worker-driven PTY + screen snapshot. */
export interface TerminalTab extends BaseTab {
  type: TabType.TERMINAL
  status?: TerminalStatus
  /** Working directory the shell was originally spawned in. */
  shellStartDir?: string
  /** Last-known screen snapshot for fast visual restore. */
  screen?: Uint8Array
  /**
   * Cumulative PTY byte offset this tab has already applied to its
   * xterm. Seeded at hydration from the backend's screen_end_offset
   * (the offset at the end of `screen`, which equals `screen.length`
   * before the ring wraps and is larger once bytes fall off), then
   * advanced monotonically as TerminalData events arrive. Echoed back
   * to the backend as WatchTerminalEntry.after_offset on resubscribe
   * so the handler skips bytes we already have.
   */
  lastOffset?: number
  cols?: number
  rows?: number
  /** Error string from TerminalStatusChange when status is STARTUP_FAILED. */
  startupError?: string
  /** Phase label from TerminalStatusChange.startup_message while status is STARTING (e.g. "Starting zsh…"). */
  startupMessage?: string
  /**
   * True once the terminal has emitted any non-whitespace output to the
   * xterm buffer. Drives the "Starting terminal…" overlay — kept visible
   * over the mounted xterm until the shell has actually painted its
   * prompt (not just the moment the PTY was spawned). Preseeded true on
   * reconnect when a screen snapshot is restored.
   */
  contentReady?: boolean
}

/**
 * FILE tab. Path + display mode are the canonical inputs; per-file
 * git status flows through `fileTabPaths` + `gitFileStatusStore`.
 */
export interface FileTab extends BaseTab {
  type: TabType.FILE
  filePath?: string
  displayMode?: string
  fileViewMode?: FileViewMode
  fileDiffBase?: FileDiffBase
  fileOpenSource?: FileOpenSource
}

/**
 * Discriminated union of every tab kind. Narrow with `switch (tab.type)`
 * or the per-kind guards below.
 */
export type Tab = AgentTab | TerminalTab | FileTab

export function isAgentTab(t: Tab): t is AgentTab {
  return t.type === TabType.AGENT
}

export function isTerminalTab(t: Tab): t is TerminalTab {
  return t.type === TabType.TERMINAL
}

export function isFileTab(t: Tab): t is FileTab {
  return t.type === TabType.FILE
}

/** The three tab fields derived from git status. */
export type GitTabFields = Pick<BaseTab, 'gitBranch' | 'gitOriginUrl' | 'gitToplevel'>

export interface TabItemOps {
  onClose?: (tab: Tab) => void
  onRename?: (tab: Tab, title: string) => void
  closingKeys?: Set<string>
}

export interface TabStoreState {
  tabs: Tab[]
  activeTabKey: string | null
  /** Per-tile active tab keys. */
  tileActiveTabKeys: Record<string, string | null>
}

/**
 * Subset of `TabStoreState` required to restore the tab store.
 * Per-tab `mru` round-trips inside each Tab record; global +
 * per-tile MRU order is derived from `tabs`, so there are no
 * separate MRU registers to restore.
 */
export type RestorableTabState
  = Pick<TabStoreState, 'tabs' | 'activeTabKey'>
    & Partial<Pick<TabStoreState, 'tileActiveTabKeys'>>

export interface AddTabOptions {
  activate?: boolean
  afterKey?: string | null
  /**
   * Skip CRDT op emission. Used by the reconciliation path that
   * inserts a tab learned-from-projection into local state — the
   * canonical record is already in the CRDT, so re-emitting would
   * thrash the wire with LWW-no-op ops.
   */
  silent?: boolean
}

export interface RemoveTabOptions {
  /**
   * Skip CRDT op emission. Used by the reconciliation path when the
   * canonical projection no longer contains the tab (e.g. another
   * client tombstoned it).
   */
  silent?: boolean
}

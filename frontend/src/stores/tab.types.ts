import type { AgentProvider, AgentStatus, AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import type { TerminalStatus } from '~/generated/proto/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'

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
  /**
   * Owning workspace, resolved from `tile_id` by the projection rather than
   * stored on the `TabRecord`. Present on every assembled tab, which is what
   * lets a caller act on a tab without first asking which workspace it is in.
   */
  workspaceId: string
  title?: string
  hasNotification?: boolean
  position?: string
  tileId?: string
  workerId?: string
  /**
   * Local-only monotonic activation counter. Higher = more recently
   * activated. Stamped by `tabSelection.setActive`, which is the only
   * writer. Used to derive global + per-tile MRU order without parallel
   * registers.
   *
   * Not persisted in the CRDT and not part of the rendered-tab proto; it
   * orders MRU views within a single client session only.
   */
  mru?: number
  workingDir?: string
  createdAt?: string
  /**
   * Absolute working-tree root of the tab's enclosing git repository
   * (from `git rev-parse --show-toplevel`). Used to group origin-less
   * repos ("local" repos) in the sidebar tree; the same toplevel means
   * the same repo, different toplevels mean different repos.
   */
  gitToplevel?: string
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
  // Current (optimistically-updated) selections, keyed by option-group id. Every
  // axis -- model, effort, permission mode, and provider-specific options alike --
  // lives here; there are no special-cased per-axis fields.
  optionValues?: Record<string, string>
  // Full option-group catalog (model/effort/permission/provider axes) reported by the agent.
  optionGroups?: AvailableOptionGroup[]
  /**
   * Error string carried while AgentStatus.STARTUP_FAILED so the chat
   * startup banner can render the agent's failure reason.
   */
  startupError?: string
  /** Phase label carried while AgentStatus.STARTING (e.g. "Starting Claude…"). */
  startupMessage?: string
  /**
   * Subagent linkage. parentAgentId is set only for virtual child agents
   * (subagent transcripts fed by the parent provider's process). A child tab
   * never owns a process; close is tab-only and the registry resolves through
   * the root.
   */
  parentAgentId?: string
  /** Whether this agent accepts a message sent directly to it (composer gate). */
  acceptsMessages?: boolean
  /**
   * The ROOT owner agent id (top of the parentAgentId chain). Equals the tab's
   * own id for a root. Set on hydration from AgentInfo.root_agent_id; the
   * background-task registry and the root's NOTIFY events key off it.
   */
  rootAgentId?: string
}

/** TERMINAL tab. Worker-driven PTY + screen snapshot. */
export interface TerminalTab extends BaseTab {
  type: TabType.TERMINAL
  status?: TerminalStatus
  /** Working directory the shell was originally spawned in. */
  shellStartDir?: string
  /** Last-known screen snapshot for fast visual restore. */
  screen?: Uint8Array
  // `lastOffset` is deliberately NOT here -- see `TerminalMeta.lastOffset`.
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
  /** PTY-driven title from worker-side OSC parsing; tab strip falls back before generic label. */
  ptyTitle?: string
  progressState?: import('~/generated/proto/leapmux/v1/terminal_pb').TerminalProgress_State
  progressPercent?: number
}

/**
 * FILE tab. Path + display mode are the canonical inputs; per-file
 * git status flows through the repo-keyed {@link createRepoGitStore}.
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
 * IMAGE tab. One image an agent returned inside one chat message, opened on its
 * own so it can be zoomed.
 *
 * A REFERENCE, not the bytes: `(agentId, seq, imageIndex)` addresses the image
 * and the client resolves it from the chat store, or with the one
 * `GetAgentMessage` the scroll rail already makes for its own previews. Copying
 * a screenshot into tab state would put it in a layer that is swept, mirrored
 * and persisted for reasons that have nothing to do with images.
 */
export interface ImageTab extends BaseTab {
  type: TabType.IMAGE
  imageAgentId?: string
  imageSeq?: bigint
  imageIndex?: number
}

/**
 * Discriminated union of every tab kind. Narrow with `switch (tab.type)`
 * or the per-kind guards below.
 */
export type Tab = AgentTab | TerminalTab | FileTab | ImageTab

export function isAgentTab(t: Tab): t is AgentTab {
  return t.type === TabType.AGENT
}

export function isTerminalTab(t: Tab): t is TerminalTab {
  return t.type === TabType.TERMINAL
}

export function isFileTab(t: Tab): t is FileTab {
  return t.type === TabType.FILE
}

export function isImageTab(t: Tab): t is ImageTab {
  return t.type === TabType.IMAGE
}

/**
 * True for the tab kinds whose contents live in a worker-side `TabPayload`
 * rather than in the CRDT.
 *
 * AGENT and TERMINAL tabs are resolved by their own worker RPCs (ListAgents /
 * ListTerminals) against ids the server minted. FILE and IMAGE tabs carry
 * client-minted ids and a payload the hub must never see, so they share one
 * register / replay / re-read / revoke path -- and every consumer of that path
 * asks this question rather than listing the two types again.
 *
 * The user-facing rules follow from the same property, which is why they ask it
 * too rather than growing their own list: such a tab has no server-side name to
 * rename (its title comes from the payload), it owns no process, and closing it
 * is a row delete -- so a read-only viewer may still close one.
 */
export function isPayloadBackedTabType(type: TabType): boolean {
  return type === TabType.FILE || type === TabType.IMAGE
}

/** The tab field that links a tab to its repo-keyed git store entry. */
export type GitTabFields = Pick<BaseTab, 'gitToplevel'>

export interface TabItemOps {
  onClose?: (tab: Tab) => void
  onRename?: (tab: Tab, title: string) => void
  closingKeys?: Set<string>
}

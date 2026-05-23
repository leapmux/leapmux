import type { AgentTab, BaseTab, GitTabFields, Tab, TerminalTab } from './tab.types'
import type { listTerminals } from '~/api/workerRpc'
import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { basename } from '~/lib/paths'
import { updateSettingsLabelCache } from '~/lib/settingsLabelCache'

/**
 * Module note: pure helpers over `Tab` records — no signals, no
 * imperative API. Lives in its own module so test code can import
 * just `tabKey` / `parseTabKey` / proto-to-tab converters without
 * dragging in the store factory's reactive dependencies.
 */

type ProtoTerminal = Awaited<ReturnType<typeof listTerminals>>['terminals'][number]

/**
 * Repository-identity equality for matching a (workerId, repoToplevel)
 * pair against a Tab-shaped value. Used by:
 *  - AppShell's branch-changed routing to decide whether to refresh the
 *    gitFileStatusStore singleton (only when the changed repo is the
 *    active tab's repo).
 *  - `tabStore.stampBranchOnTabs` to find every tab in the same repo.
 * Treats undefined workerId / gitToplevel as the empty string so a tab
 * that's never been git-resolved doesn't accidentally match an empty
 * comparison.
 */
export function isSameRepo(
  tabLike: { workerId?: string, gitToplevel?: string } | null | undefined,
  workerId: string,
  repoToplevel: string,
): boolean {
  if (!tabLike)
    return false
  return (tabLike.workerId ?? '') === workerId && (tabLike.gitToplevel ?? '') === repoToplevel
}

/**
 * Worker-provided fields for a terminal tab, ready to spread into a `Tab`
 * or pass to `updateTab`. Excludes layout-specific fields (`type`, `id`,
 * `tileId`, `position`) which the caller controls.
 */
export function protoToTerminalTabFields(workerId: string, term: ProtoTerminal): Partial<TerminalTab> {
  const status: TerminalStatus
    = term.status === TerminalStatus.READY && term.exited ? TerminalStatus.EXITED : term.status
  return {
    title: term.title || undefined,
    workerId,
    workingDir: term.workingDir || undefined,
    shellStartDir: term.shellStartDir || undefined,
    screen: term.screen.length > 0 ? term.screen : undefined,
    lastOffset: term.screen.length > 0 ? Number(term.screenEndOffset) : undefined,
    cols: term.cols || undefined,
    rows: term.rows || undefined,
    gitBranch: term.gitBranch || undefined,
    gitOriginUrl: term.gitOriginUrl || undefined,
    gitToplevel: term.gitToplevel || undefined,
    gitIsWorktree: term.gitIsWorktree || undefined,
    status,
    startupError: term.startupError || undefined,
    startupMessage: term.startupMessage || undefined,
    // Any persisted screen means the shell already painted content; an
    // exited DB-only terminal has no future data source, so it must not
    // remain covered by the startup overlay either.
    contentReady: term.screen.length > 0 || term.exited ? true : undefined,
  }
}

/** Build a terminal `Tab` from a `listTerminals` proto record. */
export function protoToTerminalTab(workerId: string, term: ProtoTerminal): TerminalTab {
  return {
    type: TabType.TERMINAL,
    id: term.terminalId,
    ...protoToTerminalTabFields(workerId, term),
  }
}

/**
 * Worker-provided fields for an agent tab, ready to spread into a `Tab`
 * or pass to `updateTab`. Excludes layout-specific fields (`type`, `id`,
 * `tileId`, `position`) which the caller controls.
 *
 * Side effect: routes the agent's available models / option groups into
 * `settingsLabelCache` so settings-related notifications can render
 * display names without carrying the full catalogs on every tab read.
 */
export function protoToAgentTabFields(workerId: string, agent: AgentInfo): Partial<AgentTab> {
  if ((agent.availableModels && agent.availableModels.length > 0) || (agent.availableOptionGroups && agent.availableOptionGroups.length > 0))
    updateSettingsLabelCache(agent.availableModels, agent.availableOptionGroups)
  return {
    title: agent.title || undefined,
    workerId,
    workingDir: agent.workingDir,
    agentProvider: agent.agentProvider,
    agentStatus: agent.status,
    agentSessionId: agent.agentSessionId || undefined,
    model: agent.model || undefined,
    effort: agent.effort || undefined,
    permissionMode: agent.permissionMode || undefined,
    extraSettings: agent.extraSettings && Object.keys(agent.extraSettings).length > 0 ? agent.extraSettings : undefined,
    availableModels: agent.availableModels && agent.availableModels.length > 0 ? agent.availableModels : undefined,
    availableOptionGroups: agent.availableOptionGroups && agent.availableOptionGroups.length > 0 ? agent.availableOptionGroups : undefined,
    agentGitStatus: agent.gitStatus,
    createdAt: agent.createdAt || undefined,
    startupError: agent.startupError || undefined,
    startupMessage: agent.startupMessage || undefined,
    gitBranch: agent.gitStatus?.branch || undefined,
    gitOriginUrl: agent.gitStatus?.originUrl || undefined,
    gitToplevel: agent.gitStatus?.toplevel || undefined,
    gitIsWorktree: agent.gitStatus?.isWorktree || undefined,
  }
}

/** Build an agent `Tab` from a `listAgents` proto record. */
export function protoToAgentTab(workerId: string, agent: AgentInfo): AgentTab {
  return {
    type: TabType.AGENT,
    id: agent.id,
    ...protoToAgentTabFields(workerId, agent),
  }
}

/**
 * Adapter from a Tab back to an AgentInfo-shaped object. Used at the
 * shrinking number of boundary points where existing consumers (chat
 * plugins, `shouldShowThinkingIndicator`) take an AgentInfo wholesale.
 * Returns undefined when the tab isn't an AGENT or has no metadata
 * yet.
 *
 * The returned value is a structurally-compatible plain object cast
 * to AgentInfo; it omits the proto-runtime $typeName / message methods,
 * which the affected consumers do not call.
 */
export function agentTabToInfo(tab: Tab | undefined): AgentInfo | undefined {
  if (!tab || tab.type !== TabType.AGENT)
    return undefined
  return {
    id: tab.id,
    workspaceId: '',
    workerId: tab.workerId ?? '',
    workerName: '',
    workingDir: tab.workingDir ?? '',
    title: tab.title ?? '',
    agentProvider: tab.agentProvider!,
    status: tab.agentStatus ?? AgentStatus.UNSPECIFIED,
    model: tab.model ?? '',
    effort: tab.effort ?? '',
    permissionMode: tab.permissionMode ?? '',
    extraSettings: tab.extraSettings ?? {},
    agentSessionId: tab.agentSessionId ?? '',
    availableModels: tab.availableModels ?? [],
    availableOptionGroups: tab.availableOptionGroups ?? [],
    gitStatus: tab.agentGitStatus,
    createdAt: tab.createdAt ?? '',
    closedAt: '',
    homeDir: '',
    startupError: tab.startupError ?? '',
    startupMessage: tab.startupMessage ?? '',
  } as AgentInfo
}

/**
 * Normalize a git-info tuple (from AgentGitStatus or a flat
 * TerminalStatusChange) into the tab shape, mapping empty strings to
 * undefined so comparisons stay sane. `isWorktree` collapses `false`
 * to `undefined`: the field is read with `?? false` everywhere on the
 * tab side (see `gitTabFieldsDiffer`, `BranchGroup.isWorktree`), so
 * `false` and `undefined` are observationally identical, and storing
 * the proto zero would just churn `===`-based equality checks without
 * adding information. Callers that need to distinguish "probed but not
 * a worktree" from "never probed" must source that distinction from a
 * dedicated probe; the broadcast/refresh path doesn't carry it.
 */
export function toGitTabFields(branch: string, originUrl: string, toplevel: string, isWorktree: boolean): GitTabFields {
  return {
    gitBranch: branch || undefined,
    gitOriginUrl: originUrl || undefined,
    gitToplevel: toplevel || undefined,
    gitIsWorktree: isWorktree || undefined,
  }
}

/** True when `next` would change any of the four git fields on `tab`. */
export function gitTabFieldsDiffer(tab: GitTabFields, next: GitTabFields): boolean {
  return tab.gitBranch !== next.gitBranch
    || tab.gitOriginUrl !== next.gitOriginUrl
    || tab.gitToplevel !== next.gitToplevel
    || (tab.gitIsWorktree ?? false) !== (next.gitIsWorktree ?? false)
}

/**
 * Directory whose git status determines a tab's branch/origin. Mirror of
 * `gitutil.ResolveGitDir` on the backend — both sides must resolve the
 * same way so `resolveOptimisticGitInfo`'s dir-match guard stays correct.
 * Agent tabs never carry a shellStartDir so this collapses to workingDir
 * for them.
 */
function effectiveGitDir(tab: { shellStartDir?: string, workingDir?: string }): string {
  return tab.shellStartDir || tab.workingDir || ''
}

/**
 * Fills in empty gitBranch / gitOriginUrl on a fresh server-provided fields
 * record from the previous tab's values. Guards against a transient
 * git-status failure on the worker (nil gs from BatchGetGitStatus) wiping
 * out authoritative values the tab already had, which would drop the tab
 * out of its sidebar group until the next workspace reload. Per-field so
 * one legitimately-cleared field (e.g. user removed `origin` remote) still
 * updates instead of being masked by the preserved branch.
 *
 * Callers: the hydration/refresh paths that rebuild tabs from ListTerminals
 * / ListAgents responses. The TerminalStatusChange / statusChange handlers
 * already guard on `(branch || origin)` so they intentionally skip empty
 * broadcasts without needing this helper.
 */
export function preserveNonEmptyGitFields<T extends Partial<BaseTab>>(
  fresh: T,
  previous: Pick<BaseTab, 'gitBranch' | 'gitOriginUrl' | 'gitToplevel' | 'gitIsWorktree'> | null | undefined,
): T {
  if (!previous)
    return fresh
  const next: T = { ...fresh }
  if (!next.gitBranch && previous.gitBranch)
    next.gitBranch = previous.gitBranch
  if (!next.gitOriginUrl && previous.gitOriginUrl)
    next.gitOriginUrl = previous.gitOriginUrl
  // Carry gitToplevel + gitIsWorktree together: they're co-derived
  // from a single rev-parse, so a transient probe failure that wipes
  // toplevel must also forget the disposition (the next probe will
  // refill both). Only restore both when the fresh record has neither.
  if (!next.gitToplevel && previous.gitToplevel) {
    next.gitToplevel = previous.gitToplevel
    if (next.gitIsWorktree === undefined && previous.gitIsWorktree !== undefined)
      next.gitIsWorktree = previous.gitIsWorktree
  }
  return next
}

/**
 * Preserve client-only visual state when a worker rehydration payload has
 * empty fields because the PTY vanished before shutdown could persist a
 * final snapshot. This keeps a backend restart from erasing the tab title or
 * re-showing the startup overlay over an xterm that had already painted.
 */
export function preserveTerminalDisplayFields(
  fresh: Partial<TerminalTab>,
  previous: Pick<TerminalTab, 'title' | 'screen' | 'lastOffset' | 'contentReady'> | null | undefined,
): Partial<TerminalTab> {
  if (!previous)
    return fresh
  const next: Partial<TerminalTab> = { ...fresh }
  if (!next.title && previous.title)
    next.title = previous.title
  if (!next.screen && previous.screen && previous.screen.length > 0)
    next.screen = previous.screen
  if (next.lastOffset === undefined && previous.lastOffset !== undefined)
    next.lastOffset = previous.lastOffset
  if (next.contentReady === undefined && previous.contentReady)
    next.contentReady = true
  return next
}

/**
 * Optimistic git branch/origin to seed on a freshly-opened agent or terminal
 * tab. A new tab starts with empty gitBranch/gitOriginUrl and only learns
 * them once the async phase-1 startup broadcasts TerminalStatusChange; in
 * that window the sidebar renders the tab under the workspace instead of
 * nested under its branch (WorkspaceTabTree.buildTree groups solely on
 * gitOriginUrl). Seeding avoids that flash.
 *
 * Only safe to seed when the active tab and the new tab resolve to the same
 * git directory — otherwise the seeded values would be wrong for the new
 * tab's repo. File tabs have no authoritative git info so they never seed.
 */
export function resolveOptimisticGitInfo(
  activeTab: Tab | null | undefined,
  newTab: { shellStartDir?: string, workingDir?: string },
): { gitBranch?: string, gitOriginUrl?: string, gitToplevel?: string, gitIsWorktree?: boolean } {
  if (!activeTab)
    return {}
  if (activeTab.type !== TabType.AGENT && activeTab.type !== TabType.TERMINAL)
    return {}
  // Needs at least an origin or a toplevel — otherwise there is no grouping
  // value to seed, and the sidebar would still fall through to ungrouped
  // until the authoritative broadcast arrives.
  if (!activeTab.gitOriginUrl && !activeTab.gitToplevel)
    return {}
  const activeDir = effectiveGitDir(activeTab)
  const newDir = effectiveGitDir(newTab)
  if (!activeDir || activeDir !== newDir)
    return {}
  return {
    gitBranch: activeTab.gitBranch || undefined,
    gitOriginUrl: activeTab.gitOriginUrl || undefined,
    gitToplevel: activeTab.gitToplevel || undefined,
    gitIsWorktree: activeTab.gitIsWorktree || undefined,
  }
}

/**
 * Whether the tab's working tree is in a stable state for `git status`.
 *
 * Defers across the entire STARTING window of a worktree-creating agent
 * or terminal. While `git worktree add` is still effectively populating
 * the working tree (or its writes are not yet observable to a separate
 * process running `git status` — seen in practice on at least one
 * filesystem setup), a status query reports every still-unwritten
 * in-index file as deleted, which would otherwise blast bogus diff
 * stats onto the new tab. Waiting for status to leave STARTING is the
 * conservative signal that's known to be reliable: by then phase 2's
 * provider init has completed too, and the worktree has had time to
 * settle.
 *
 * Trade-off: the file tree shows no diff badge for the whole startup
 * (a few seconds), not just phase 0. Acceptable — users don't expect
 * meaningful diff stats while "Starting <provider>…" is on screen.
 *
 * File tabs are always treated as ready — they don't go through the
 * worktree-creating startup pipeline.
 */
export function isTabReadyForGitStatus(
  tab: Tab | null | undefined,
  agent: Pick<AgentInfo, 'status' | 'startupMessage' | 'gitStatus'> | null | undefined,
): boolean {
  if (!tab)
    return true
  if (tab.type === TabType.AGENT) {
    if (!agent)
      return true
    return agent.status !== AgentStatus.STARTING
  }
  if (tab.type === TabType.TERMINAL) {
    return tab.status !== TerminalStatus.STARTING
  }
  return true
}

export function tabKey(tab: { type: TabType, id: string }): string {
  return `${tab.type}:${tab.id}`
}

/**
 * Human-readable label for a tab. Prefer `tab.title` (server-set or
 * user-renamed); fall back to a type-aware default. FILE tabs derive
 * their label from `basename(filePath)` so the workspace tree and the
 * tab strip stay in sync — both surfaces show the same name once the
 * worker's path hydrator has filled `filePath` in.
 */
export function tabDisplayLabel(tab: Tab): string {
  if (tab.title)
    return tab.title
  if (tab.type === TabType.FILE)
    return (tab.filePath ? basename(tab.filePath) : '') || 'File'
  return tab.type === TabType.AGENT ? 'Agent' : 'Terminal'
}

/**
 * Build an O(1) lookup map of tabs by `tabKey`. Used by hydration paths
 * that need to merge fresh server data with the previous snapshot's
 * client-only fields (preserveNonEmptyGitFields, preserveTerminalDisplayFields).
 */
export function tabsByKey(tabs: readonly Tab[]): Map<string, Tab> {
  const m = new Map<string, Tab>()
  for (const t of tabs)
    m.set(tabKey(t), t)
  return m
}

/**
 * Inverse of `tabKey`. Returns null when the input is malformed (missing
 * colon, non-numeric type) so callers can decide how to handle stale or
 * corrupt persisted keys.
 */
export function parseTabKey(key: string): { type: TabType, id: string } | null {
  const idx = key.indexOf(':')
  if (idx <= 0 || idx === key.length - 1)
    return null
  const typeNum = Number(key.slice(0, idx))
  if (!Number.isInteger(typeNum))
    return null
  return { type: typeNum as TabType, id: key.slice(idx + 1) }
}

export function canCloseTab(readOnly: boolean | undefined, tab: Tab): boolean {
  return !readOnly || tab.type === TabType.FILE
}

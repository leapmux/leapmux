import type { AgentTab, GitTabFields, Tab, TerminalTab } from './tab.types'
import type { TerminalMeta } from './tabMetadata.store'
import type { listTerminals } from '~/api/workerRpc'
import type { AgentGitStatus, AgentInfo, AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import { effectiveCurrent, OPTION_ID_MODEL, optionGroup } from '~/components/chat/settingsGroups'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { TerminalProgress_State, TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { basename } from '~/lib/paths'
import { updateSettingsLabelCache } from '~/lib/settingsLabelCache'
import { isTerminalTab } from './tab.types'

/**
 * Module note: pure helpers over `Tab` records — no signals, no
 * imperative API. Lives in its own module so test code can import
 * just `tabKey` / `parseTabKey` / proto-to-tab converters without
 * dragging in the store factory's reactive dependencies.
 */

type ProtoTerminal = Awaited<ReturnType<typeof listTerminals>>['terminals'][number]

/**
 * The identity of one repository checkout on one worker.
 *
 * The two fields travel together through the whole branch flow — the sidebar
 * row that opens a dialog, the dialog's payload, `onBranchChanged`,
 * `handleBranchChanged`, `stampBranchOnTabs` and `isSameRepo`. As two
 * adjacent same-typed strings they were transposable at every hop, and a
 * transposition compiles and then matches nothing, so the branch stamp
 * silently reaches zero tabs. One parameter makes that mistake
 * unrepresentable.
 */
export interface RepoRef {
  workerId: string
  /**
   * `git rev-parse --show-toplevel`. For a main repo this is the repo root;
   * for a linked worktree it is the worktree root. Matches `Tab.gitToplevel`.
   */
  gitToplevel: string
}

/**
 * Repository-identity equality for matching a {@link RepoRef} against a
 * Tab-shaped value. Used by:
 *  - AppShell's branch-changed routing to decide whether to refresh the
 *    gitFileStatusStore singleton (only when the changed repo is the
 *    active tab's repo).
 *  - AppShell's branch stamp, to find every tab in the same repo.
 *
 * An empty `workerId` or `repoToplevel` never matches. Without those guards the
 * `?? ''` coercions below turn each into a WILDCARD over every tab whose git
 * identity hasn't been resolved yet: a branch change in one un-stamped repo
 * would re-label tabs in a sibling un-stamped repo on the same worker, and an
 * unresolved worker would match every such tab regardless of worker. Callers
 * must resolve both first. (The stamp now spans every workspace, not just the
 * active one, so the blast radius of that leak is the whole account.)
 *
 * BOTH halves are guarded HERE. The worker half used to live at one call site
 * — `stampBranchOnTabs`, whose own doc said the two halves need the guard "for
 * the same reason" — which left the predicate itself answering `true` for
 * `('' === '')` and made every future caller responsible for remembering. A
 * guard that only one caller applies is a guard the next caller does not have.
 */
export function isSameRepo(
  tabLike: { workerId?: string, gitToplevel?: string } | null | undefined,
  repo: RepoRef,
): boolean {
  if (!tabLike || !repo.workerId || !repo.gitToplevel)
    return false
  return (tabLike.workerId ?? '') === repo.workerId && (tabLike.gitToplevel ?? '') === repo.gitToplevel
}

/**
 * Worker-provided fields for a terminal tab, ready to patch into `tabMetadata`.
 * Excludes layout-specific fields (`type`, `id`, `tileId`, `position`), which
 * come from the projection rather than the worker.
 */
export function protoToTerminalTabFields(workerId: string, term: ProtoTerminal): Partial<TerminalTab> & Pick<TerminalMeta, 'lastOffset'> {
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
    // The four git fields as one group, through the shared normalizer, so this
    // producer cannot answer "no answer" differently from the other two.
    // Real values, NOT collapsed to undefined -- `tabMetadata.patch` skips
    // undefined, and this producer runs a SECOND time over a populated row:
    // `useTabHydrators` re-arms on DISCONNECTED, which the worker-offline sweep
    // sets. Collapsing the negative cases means a terminal that stopped being a
    // worktree, or finished starting, can never be told. (`title` above is
    // deliberately NOT in this group: an empty OSC title must leave a user's
    // rename alone.)
    ...toGitTabFields(term.gitBranch, term.gitOriginUrl, term.gitToplevel, term.gitIsWorktree),
    status,
    startupError: term.startupError,
    startupMessage: term.startupMessage,
    // Any persisted screen means the shell already painted content; an
    // exited DB-only terminal has no future data source, so it must not
    // remain covered by the startup overlay either.
    contentReady: term.screen.length > 0 || term.exited ? true : undefined,
  }
}

/**
 * Derive the tab's current-selection fields from an agent's option-group catalog:
 * every group's current value is collected into the ONE `optionValues` map keyed by
 * group id (model/effort/permissionMode and provider options alike -- there are no
 * special-cased per-axis fields), and the full `optionGroups` catalog is carried
 * alongside. A group whose current value is empty is simply absent from the map,
 * which the panel reads as "not reported" and falls back to the group's default.
 * Shared by the hydration path (`protoToAgentTabFields`) and the live `statusChange`
 * handler so both derive the tab's current selections the same way.
 *
 * Pure: the caller is responsible for priming `settingsLabelCache` from the same
 * groups (via `updateSettingsLabelCache`) at the data-ingestion boundary -- see
 * `protoToAgentTabFields` and the `statusChange` handler in useWorkspaceConnection.
 */
export function deriveOptionGroupTabFields(groups: AvailableOptionGroup[] | undefined): Partial<AgentTab> {
  if (!groups || groups.length === 0)
    return {}
  const optionValues = optionValuesFromGroups(groups)
  const fields: Partial<AgentTab> = { optionGroups: groups }
  // Only attach optionValues when at least one group reports a current value.
  // Returning `optionValues: undefined` would, when spread into the tab, clear
  // the previously-derived selections on a push whose groups carry no currents.
  if (Object.keys(optionValues).length > 0)
    fields.optionValues = optionValues
  return fields
}

/**
 * Collect every group's confirmed `currentValue` into a flat id->value map (the
 * non-empty ones). This is the generic, axis-agnostic counterpart to the catalog:
 * model/effort/permission and provider extras all live in one map keyed by group id.
 */
export function optionValuesFromGroups(groups: AvailableOptionGroup[] | undefined): Record<string, string> {
  const values: Record<string, string> = {}
  for (const g of groups ?? []) {
    if (g.currentValue)
      values[g.id] = g.currentValue
  }
  return values
}

/**
 * Write a single axis into an id->value option map, returning a fresh map. An empty
 * value DELETES the key rather than storing '' . This enforces the invariant that
 * `optionValues` never holds an empty string: `agentTabOptionGroups` treats a stored
 * '' as a real override that blanks the group's selection (showing its default) instead
 * of falling through to the catalog's confirmed currentValue. Routing every optimistic
 * write through here makes that invariant mechanical rather than convention-only.
 */
export function setOptionValue(map: Record<string, string> | undefined, id: string, value: string): Record<string, string> {
  const next = { ...(map ?? {}) }
  if (value)
    next[id] = value
  else
    delete next[id]
  return next
}

/**
 * Worker-provided fields for an agent tab, ready to patch into `tabMetadata`.
 * Excludes layout-specific fields (`type`, `id`, `tileId`, `position`), which
 * come from the projection rather than the worker.
 *
 * Ingestion point: primes `settingsLabelCache` from the agent's option groups so
 * settings-related notifications can render display names without carrying the full
 * catalogs on every tab read. (The pure `deriveOptionGroupTabFields` no longer does
 * this itself.)
 *
 */
export function protoToAgentTabFields(workerId: string, agent: AgentInfo): Partial<AgentTab> {
  updateSettingsLabelCache(agent.agentProvider, agent.optionGroups)
  return {
    title: agent.title || undefined,
    workerId,
    workingDir: agent.workingDir,
    agentProvider: agent.agentProvider,
    agentStatus: agent.status,
    agentSessionId: agent.agentSessionId || undefined,
    ...deriveOptionGroupTabFields(agent.optionGroups),
    createdAt: agent.createdAt || undefined,
    startupError: agent.startupError || undefined,
    startupMessage: agent.startupMessage || undefined,
    // The whole git group as one unit, through the shared producer. Both agent
    // producers of this group must agree about what "no answer" is and about
    // reference reuse, and the only way to make that true rather than merely
    // asserted is for the answer to live in one place.
    ...toAgentGitTabFields(agent.gitStatus),
  }
}

/**
 * Swap the model-dependent groups (effort, and Claude's extended-thinking group
 * whose enabled label is "Adaptive" vs "On" per model) for the ones the selected
 * model carries in its `subGroups`. This lets an optimistic model switch update
 * those groups instantly, instead of waiting for the worker's relaunch
 * round-trip (a model change resets effort to auto, which forces a relaunch).
 *
 * The dependent group ids are the union across every model's sub_groups, so a
 * model that omits one (Haiku has no effort group) correctly drops it. Returns a
 * new array sorted by display order; the sub_group objects themselves are stable
 * references from the catalog, so `<For>` reconciliation doesn't churn the DOM.
 */
function withSelectedModelSubGroups(groups: AvailableOptionGroup[], selectedModelId: string): AvailableOptionGroup[] {
  const modelOptions = optionGroup(groups, OPTION_ID_MODEL)?.options ?? []
  const dependentIds = new Set(modelOptions.flatMap(o => o.subGroups.map(g => g.id)))
  if (dependentIds.size === 0)
    return groups
  const selected = modelOptions.find(o => o.id === selectedModelId)
  // If the optimistic model isn't a listed option (e.g. a hidden id that lingers
  // in optionValues), keep the catalog's existing dependent groups rather than
  // stripping them to nothing -- otherwise effort/thinking vanish until the next
  // push.
  if (!selected)
    return groups
  const kept = groups.filter(g => !dependentIds.has(g.id))
  return [...kept, ...selected.subGroups].sort((a, b) => a.order - b.order)
}

// Cache the optimistic projection per (optionGroups, optionValues) reference pair. Both keys are
// reference-stable until their CONTENT changes -- optionGroups via mergeStableOptionGroupRefs (it
// reuses the prior array when a push is content-identical), optionValues because every edit
// replaces it wholesale ({ ...prev, ...delta }). So repeated reads during an in-flight model switch
// (whose inputs hold steady until the worker confirms) return the SAME projected array instead of
// rebuilding the model-dependent groups on every render, while a real content change still flows
// through (new reference -> cache miss -> recompute). A WeakMap keyed on the optionGroups array
// auto-evicts when that array is replaced, so there is nothing to invalidate by hand.
const optionGroupsProjectionCache = new WeakMap<
  AvailableOptionGroup[],
  { values: Record<string, string> | undefined, result: AvailableOptionGroup[] }
>()

/**
 * Project the tab's option-group catalog with each group's `currentValue`
 * overlaid by the tab's optimistically-updated selection from `optionValues`
 * (one generic map keyed by group id -- model/effort/permission/extras alike).
 * This keeps the read model (option groups) in lockstep with an in-flight
 * settings change, so the panel and trigger label reflect a click immediately
 * rather than waiting for the worker's status round-trip.
 *
 * Returns the SAME array reference when no group needs an override (the steady
 * state, once the worker confirms the change) AND across repeated reads while an
 * optimistic switch is in flight (via optionGroupsProjectionCache), so downstream
 * `<For>` rendering doesn't churn its DOM and Playwright/users get a stable click target.
 */
function agentTabOptionGroups(tab: AgentTab): AvailableOptionGroup[] {
  const base = tab.optionGroups ?? []
  const values = tab.optionValues
  const cached = optionGroupsProjectionCache.get(base)
  if (cached && cached.values === values)
    return cached.result
  const result = projectOptionGroups(base, values)
  optionGroupsProjectionCache.set(base, { values, result })
  return result
}

function projectOptionGroups(base: AvailableOptionGroup[], values: Record<string, string> | undefined): AvailableOptionGroup[] {
  // Optimistic model switch: while the user's model click is still in flight
  // (the optimistic model differs from the catalog's confirmed model), rebuild the
  // model-dependent groups from the newly-selected model's sub_groups so effort
  // and thinking update immediately rather than after the relaunch round-trip.
  // OPTION_ID_MODEL is a legitimate domain reference here (the model group is the
  // one that carries sub_groups), not a stored-value special-case.
  const optimisticModel = values?.[OPTION_ID_MODEL]
  const modelGroup = optionGroup(base, OPTION_ID_MODEL)
  const groups = optimisticModel && modelGroup && optimisticModel !== modelGroup.currentValue
    ? withSelectedModelSubGroups(base, optimisticModel)
    : base

  let changed = groups !== base
  const out = groups.map((g) => {
    // Same optimistic-over-confirmed PRECEDENCE as the panel's currentForGroup (the shared
    // effectiveCurrent helper). This projects the RAW optimistic value; the panel and trigger
    // additionally CLAMP/validate an out-of-list value (currentValueOrDefault / effortValid),
    // so during an in-flight model switch the effort group's projected currentValue may briefly
    // be a tier the new model doesn't offer (e.g. xhigh left over from Opus after switching to
    // Sonnet) -- every consumer that surfaces it guards against that itself. Reuse the existing
    // reference when nothing changed (DOM stability).
    const next = effectiveCurrent(values, g)
    if (next === g.currentValue)
      return g
    changed = true
    return { ...g, currentValue: next }
  })
  return changed ? out : base
}

/**
 * Adapter from a Tab back to an AgentInfo-shaped object. Used at the
 * shrinking number of boundary points where existing consumers (chat
 * plugins, `shouldShowThinkingIndicator`) take an AgentInfo wholesale.
 * Returns undefined when the tab isn't an AGENT or has no metadata
 * yet.
 *
 * The returned value is a structurally-compatible plain object cast to AgentInfo; it
 * omits the proto-runtime $typeName / message methods, which the affected consumers do
 * not call. We deliberately do NOT build this via the proto `create()`: create()
 * normalizes the repeated `optionGroups` field into a fresh array, which would discard
 * the reference identity agentTabOptionGroups carefully preserves (returning the SAME
 * array when nothing changed) and churn the downstream `<For>` rows on every push.
 */
export function agentTabToInfo(tab: Tab | undefined): AgentInfo | undefined {
  if (!tab || tab.type !== TabType.AGENT)
    return undefined
  return {
    id: tab.id,
    workerId: tab.workerId ?? '',
    workerName: '',
    workingDir: tab.workingDir ?? '',
    title: tab.title ?? '',
    agentProvider: tab.agentProvider!,
    status: tab.agentStatus ?? AgentStatus.UNSPECIFIED,
    agentSessionId: tab.agentSessionId ?? '',
    optionGroups: agentTabOptionGroups(tab),
    gitStatus: tab.agentGitStatus,
    createdAt: tab.createdAt ?? '',
    closedAt: '',
    homeDir: '',
    startupError: tab.startupError ?? '',
    startupMessage: tab.startupMessage ?? '',
  } as AgentInfo
}

/**
 * THE normalizer for the four git fields, from either producer shape
 * (`AgentGitStatus` or a flat `TerminalStatusChange`). Returns `undefined` when
 * the probe produced no answer at all, so callers say nothing rather than
 * asserting a negative.
 *
 * Two rules, and they are the same rule applied to all four fields.
 *
 * NO ANSWER IS NOT A NEGATIVE. The worker leaves all four at proto zero when
 * the probe returns nothing (`gitutil.GetGitStatus` yields nil when both
 * porcelain probes fail, and its caller only assigns `if gs != nil`). Writing
 * `isWorktree: false` into that is an assertion the worker never made — the tab
 * keeps its branch but loses its worktree disposition, so the sidebar regroups
 * it under the non-worktree branch row and ChangeBranchDialog offers an
 * in-place checkout on a worktree, with nothing left to re-probe it. One gate
 * over the whole group, here, so the three producers cannot each decide
 * differently: they used to, and the comment claiming they agreed was written
 * while they did not.
 *
 * AN EMPTY ANSWER IS A REAL VALUE, so the strings stay `''` rather than
 * collapsing to `undefined`. `tabMetadata.patch` SKIPS undefined by design — a
 * partial row must not blank fields another source owns — so a collapsed field
 * cannot clear a populated one: `patch` drops the undefined, nothing changes,
 * and the next status event repeats that forever. A repo that loses its remote would keep the dead origin in the
 * sidebar's grouping for the life of the page. `applyGitStatusToTabs` reached
 * the same conclusion independently and already sends raw `''`; this is the
 * other half of that agreement.
 */
export function toGitTabFields(branch: string, originUrl: string, toplevel: string, isWorktree: boolean): GitTabFields | undefined {
  if (!branch && !originUrl && !toplevel)
    return undefined
  return {
    gitBranch: branch,
    gitOriginUrl: originUrl,
    gitToplevel: toplevel,
    gitIsWorktree: isWorktree,
  }
}

/**
 * THE producer of an agent tab's git group: the full `AgentGitStatus` the info
 * card renders ahead/behind and the dirty-state flags from, plus the four flat
 * fields every other consumer reads (see `toGitTabFields`). Both agent producers
 * -- the live `statusChange` handler and the hydration/open reply -- go through
 * here, so the five fields cannot be assembled two different ways.
 *
 * Reference reuse is deliberately NOT this function's problem, though the worker
 * re-ships the whole status on every push and an unchanged repo therefore arrives
 * as an equal-but-fresh proto on every turn end. This used to take the tab's
 * current status as a `prev` argument and report `undefined` when the two matched.
 * That worked, but it made "do not re-key the tab" a rule every producer had to
 * opt into, and the opt-in leaked into the callers: one of them needed a
 * `tab.type === TabType.AGENT ? tab.agentGitStatus : undefined` dance just to
 * supply it, and another read `prev` across an `await` where a concurrent status
 * push could stale it. `tabMetadata.patch` now compares object-valued fields at
 * the single write point (see `sameStoredValue`), so an equal payload is dropped
 * there for EVERY field and every producer, including ones written later that
 * would never have known to ask.
 */
export function toAgentGitTabFields(status: AgentGitStatus | undefined): Partial<AgentTab> {
  if (!status)
    return {}
  return {
    agentGitStatus: status,
    ...toGitTabFields(status.branch, status.originUrl, status.toplevel, status.isWorktree),
  }
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
 * Optimistic git branch/origin to seed on a freshly-opened tab of ANY kind --
 * agent, terminal, or file.
 * A new tab starts with empty gitBranch/gitOriginUrl and only learns
 * them once the async phase-1 startup broadcasts TerminalStatusChange; in
 * that window the sidebar renders the tab under the workspace instead of
 * nested under its branch (WorkspaceTabTree.buildTree groups solely on
 * gitOriginUrl). Seeding avoids that flash.
 *
 * Only safe to seed when the active tab and the new tab resolve to the same
 * git directory — otherwise the seeded values would be wrong for the new
 * tab's repo.
 *
 * ANY tab kind may be the SOURCE. A file tab now carries a `workingDir` and
 * gets its git fields stamped by the same containment match as the other two,
 * so "file tabs have no authoritative git info" — the reason this used to
 * refuse them outright — stopped being true when that column landed. Nothing
 * was protecting: the two guards below already do the whole job. A tab with
 * neither origin nor toplevel has nothing to seed and returns early, and one in
 * a different directory fails the dir match. A type test on top of those could
 * only reject a tab that passes both, which is precisely a tab that would have
 * seeded correctly — a file tab opened from an agent, whose branch group is the
 * one the next file opened beside it belongs in.
 *
 * ABSENT KEYS ARE OMITTED, not set to `undefined`. Object spread copies
 * `undefined`-valued OWN keys, so a key present-but-undefined does not "leave
 * the value alone", it ERASES it. That matters most where the seed is spread
 * AFTER the worker's own fields — `useAgentOperations` (`{ ...agentFields,
 * ...seed }`) and `useTerminalOperations` (`{ ...meta, ...seed }`): an active
 * tab in the same directory whose branch has not resolved yet still passes the
 * origin/toplevel guard above, and would have wiped the authoritative
 * `gitBranch` the OpenAgent response just supplied. The FILE caller
 * (`useTabOperations.handleFileOpen`) spreads the seed FIRST instead, which is
 * safe for a different reason — the fields it writes after it (`filePath`,
 * `workingDir`, `title`) are disjoint from the four this returns, so neither
 * side can erase the other. A seed whose job is to ADD information must never
 * subtract, whichever order it lands in.
 */
export function resolveOptimisticGitInfo(
  activeTab: Tab | null | undefined,
  newTab: { shellStartDir?: string, workingDir?: string },
): { gitBranch?: string, gitOriginUrl?: string, gitToplevel?: string, gitIsWorktree?: boolean } {
  if (!activeTab)
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
  const seed: { gitBranch?: string, gitOriginUrl?: string, gitToplevel?: string, gitIsWorktree?: boolean } = {}
  if (activeTab.gitBranch)
    seed.gitBranch = activeTab.gitBranch
  if (activeTab.gitOriginUrl)
    seed.gitOriginUrl = activeTab.gitOriginUrl
  if (activeTab.gitToplevel)
    seed.gitToplevel = activeTab.gitToplevel
  if (activeTab.gitIsWorktree !== undefined)
    seed.gitIsWorktree = activeTab.gitIsWorktree
  return seed
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
 * user-renamed); for terminals fall back to `ptyTitle` (live OSC) before the
 * generic default. An empty OSC must leave a user's rename alone — see
 * `handleTerminalTitleChanged`, which only patches `ptyTitle` when non-empty.
 */
export function tabDisplayLabel(tab: Tab): string {
  if (tab.title)
    return tab.title
  if (tab.type === TabType.TERMINAL && tab.ptyTitle)
    return tab.ptyTitle
  if (tab.type === TabType.FILE)
    return (tab.filePath ? basename(tab.filePath) : '') || 'File'
  return tab.type === TabType.AGENT ? 'Agent' : 'Terminal'
}

/**
 * Tooltip text for a tab. Terminals prefer the live PTY title when present
 * (plan: render `ptyTitle` as the tooltip regardless of the strip label).
 */
export function tabTooltipText(tab: Tab): string {
  if (tab.type === TabType.TERMINAL && tab.ptyTitle)
    return tab.ptyTitle
  return tabDisplayLabel(tab)
}

/** Whether a terminal tab should show the OSC 9;4 progress affordance. */
export function terminalProgressVisible(tab: Tab): boolean {
  if (!isTerminalTab(tab) || tab.progressState === undefined)
    return false
  return tab.progressState !== TerminalProgress_State.UNSPECIFIED
}

export function terminalProgressPercent(tab: Tab): number {
  return isTerminalTab(tab) ? (tab.progressPercent ?? 0) : 0
}

export function terminalProgressIndeterminate(tab: Tab): boolean {
  return isTerminalTab(tab) && tab.progressState === TerminalProgress_State.INDETERMINATE
}

/** CSS custom property + title for the tab OSC progress bar. */
export function terminalProgressBarProps(tab: Tab): { style: { '--progress-percent': string }, title: string } {
  const percent = Math.max(0, Math.min(100, terminalProgressPercent(tab)))
  return {
    style: { '--progress-percent': `${percent}%` },
    title: terminalProgressIndeterminate(tab) ? 'In progress' : `${percent}%`,
  }
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

/**
 * `protoToTerminalTabFields` mapped into `TabMetadata`'s naming.
 *
 * The metadata row is flat and shared by every tab kind, so the terminal's
 * `status` cannot keep that name — an AGENT tab has its own status concept.
 * Every caller that patches terminal metadata goes through here so the rename
 * happens in exactly one place.
 */
export function terminalMetadata(workerId: string, term: ProtoTerminal) {
  const fields = protoToTerminalTabFields(workerId, term)
  const { status, ...rest } = fields
  return { ...rest, terminalStatus: status }
}

/**
 * Metadata seed for a terminal THIS client just opened via `OpenTerminal`.
 *
 * STARTING, never READY. `OpenTerminal` returns once the row is persisted; the
 * PTY comes up asynchronously and the worker broadcasts STARTING phase labels
 * and then READY or STARTUP_FAILED. A READY seed is not merely optimistic, it
 * is STICKY: `applyTerminalStatusChange`'s STARTING arm refuses to move a tab
 * that already reads READY, so every phase label is dropped AND the later READY
 * arm no-ops too — the lie outlives the startup it was guessing at, and with
 * `hydrated` set nothing re-asks. Seeding STARTING is self-correcting on every
 * one of those paths.
 *
 * `hydrated`: the OpenTerminal response IS the worker's answer for this tab, so
 * `useTabHydrators` must not immediately re-ask. Its reply would apply
 * `listTerminals`' DB-sourced snapshot with none of the live handler's guards —
 * dragging the terminal back to STARTING after the worker has already broadcast
 * READY, and rewinding `lastOffset` so the next resubscribe replays bytes xterm
 * has already drawn.
 *
 * One helper for both open paths (the tab-bar button and the dialogs) so the
 * two cannot disagree about the same moment in a terminal's life — they did,
 * and only one of them was right.
 */
export function openedTerminalMetadata(opts: {
  title: string
  workingDir: string
  shellStartDir?: string
}) {
  return {
    title: opts.title,
    workingDir: opts.workingDir,
    terminalStatus: TerminalStatus.STARTING,
    hydrated: true,
    // Only when the caller tracks one: `effectiveGitDir` is
    // `shellStartDir || workingDir`, and writing an unconditional value would
    // change what the optimistic git seed resolves against.
    ...(opts.shellStartDir !== undefined
      ? { shellStartDir: opts.shellStartDir || opts.workingDir }
      : {}),
  }
}

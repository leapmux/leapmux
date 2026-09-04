import type { RepoGitStore, UpsertRepoGitFromProtoOpts } from './repoGit'
import type { AgentTab, Tab, TerminalTab } from './tab.types'
import type { TerminalMeta } from './tabMetadata.store'
import type { listTerminals } from '~/api/workerRpc'
import type { AgentInfo, AgentProvider, AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import { pluginFor } from '~/components/chat/providers/registry'
import { effectiveCurrent, OPTION_ID_MODEL, optionGroup } from '~/components/chat/settingsGroups'
import { AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { TerminalProgress_State, TerminalStatus } from '~/generated/proto/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { basename } from '~/lib/paths'
import { updateSettingsLabelCache } from '~/lib/settingsLabelCache'
import { repoKey, repoKeyFromTab, upsertRepoGitFromProtoStatus } from './repoGit'
import { isPayloadBackedTabType, isTerminalTab } from './tab.types'

/**
 * Module note: helpers over `Tab` records — no signals. Lives in its own
 * module so test code can import just `tabKey` / `parseTabKey` / proto-to-tab
 * converters without dragging in the store factory's reactive dependencies.
 *
 * Every export here is pure EXCEPT these, and each one takes the store it
 * writes: `planOptimisticRepoGit` copies a branch onto a repo key when its
 * caller commits, and `protoToAgentTabFields` writes the repo entry that
 * matches the `gitToplevel` it returns (and primes the settings-label cache).
 * Add no other one without naming it here.
 */

type ProtoTerminal = Awaited<ReturnType<typeof listTerminals>>['terminals'][number]

/**
 * The identity of one repository checkout on one worker.
 *
 * The two fields travel together through the whole branch flow — the sidebar
 * row that opens a dialog, the dialog's payload, `onBranchChanged`,
 * `handleBranchChanged`, `stampBranchOnRepo` and `isSameRepo`. As two
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
 *    focused repo's git state in {@link createRepoGitStore}.
 *  - Branch stamp paths that target one repo key in the keyed store.
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
 * only, which left the predicate itself answering `true` for `('' === '')` and
 * made every future caller responsible for remembering. A guard that only one
 * caller applies is a guard the next caller does not have.
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
    ...(term.gitStatus?.toplevel ? { gitToplevel: term.gitStatus.toplevel } : {}),
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
 * `gitToplevel` is HALF of a tab's repo identity. The repo-keyed store holds
 * the other half, and the sidebar reads one from each: it groups a tab by the
 * row field and labels the group from the store. A caller that writes the row
 * and not the store files the tab under a repo with no branch name.
 *
 * So this function writes BOTH halves, and takes the store to do it. The
 * pairing used to be a rule in this comment that each caller had to remember,
 * and two of them did not. Now a caller cannot obtain the row fields without
 * supplying the store that takes the other half.
 *
 * `opts` carries the orphan-migration tip, which only a caller holding a LIVE
 * tab can compute (`migrateErrorHintFromForResolvedRepo`). A local open has no
 * tab yet and passes none.
 */
export function protoToAgentTabFields(
  store: Pick<RepoGitStore, 'get' | 'upsert' | 'clear'>,
  workerId: string,
  agent: AgentInfo,
  opts?: UpsertRepoGitFromProtoOpts,
): Partial<AgentTab> {
  updateSettingsLabelCache(agent.agentProvider, agent.optionGroups)
  // The store write lives HERE, in the same call that produces `gitToplevel`,
  // so the two halves of a tab's repo identity cannot be written apart. A
  // caller cannot obtain the row fields without handing over the store that
  // takes the other half. Both halves read the same status, so both are
  // skipped together when it carries no toplevel.
  upsertRepoGitFromProtoStatus(store, workerId, agent.gitStatus, opts)
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
    parentAgentId: agent.parentAgentId || undefined,
    acceptsMessages: agent.acceptsMessages,
    rootAgentId: agent.rootAgentId || undefined,
    ...(agent.gitStatus?.toplevel ? { gitToplevel: agent.gitStatus.toplevel } : {}),
  }
}

/**
 * Resolve an agent tab's ROOT main agent id. The wire-provided rootAgentId
 * (AgentInfo.root_agent_id, hydrated onto the tab) wins when present: the
 * backend already walked the parent_agent_id chain server-side, so the registry
 * owner is a wire-level fact and the frontend does not re-derive it from a
 * client chain that can be partially hydrated. Falls back to walking the
 * parentAgentId chain (with a visited-set cycle guard) for optimistic state
 * before hydration or a legacy tab with no rootAgentId. Returns `agentId` when
 * a parent is unknown or no parent is set.
 */
export function rootAgentIdFor(
  getAgentTab: (id: string) => AgentTab | undefined,
  agentId: string,
): string {
  const start = getAgentTab(agentId)
  if (start?.rootAgentId)
    return start.rootAgentId
  let current = agentId
  const visited = new Set<string>()
  while (true) {
    if (visited.has(current))
      return agentId // cycle guard: fall back to the input
    visited.add(current)
    const tab = getAgentTab(current)
    if (tab?.rootAgentId)
      return tab.rootAgentId
    if (!tab || !tab.parentAgentId)
      return current
    current = tab.parentAgentId
  }
}

/**
 * Every agent tab BELOW `agentId` in the parent chain, deepest first.
 *
 * A child agent tab owns no process: it is a transcript the parent's provider
 * feeds, and the registry resolves it through the root. So it cannot outlive
 * the tab that spawned it in any useful way -- with the parent gone, the tab
 * strip keeps a transcript nothing can add to, and `nestSubagentTabs` promotes
 * it to a top-level row that claims a lineage the user can no longer see.
 * Closing a parent therefore closes its whole subtree.
 *
 * Deepest first, so each tab closes before the parent that placed it. Grandchildren
 * are included: the walk follows the chain rather than one level of it.
 *
 * `agentId` itself is never in the result -- the caller closes that one. A cycle
 * cannot come off the wire (parent_agent_id is a DAG rooted at a main agent),
 * but the visited set makes the walk finish anyway rather than recurse forever.
 */
export function descendantAgentTabs(tabs: readonly Tab[], agentId: string): AgentTab[] {
  const childrenByParent = new Map<string, AgentTab[]>()
  for (const tab of tabs) {
    if (tab.type !== TabType.AGENT || !tab.parentAgentId)
      continue
    const siblings = childrenByParent.get(tab.parentAgentId)
    if (siblings)
      siblings.push(tab)
    else
      childrenByParent.set(tab.parentAgentId, [tab])
  }

  const out: AgentTab[] = []
  const visited = new Set<string>([agentId])
  // Depth-first, appending each tab AFTER its own descendants, so the result
  // reads deepest first.
  const walk = (parentId: string) => {
    for (const child of childrenByParent.get(parentId) ?? []) {
      if (visited.has(child.id))
        continue
      visited.add(child.id)
      walk(child.id)
      out.push(child)
    }
  }
  walk(agentId)
  return out
}

/**
 * Whether an agent tab accepts a user message (its composer is enabled). Roots
 * always accept. Children accept only when their feeding provider can steer a
 * subagent conversation (acceptsMessages === true); a non-steerable child is a
 * read-only transcript. Used to exclude non-steerable children from MRU-agent
 * resolution (file mentions/quotes never target a disabled composer), and to
 * decide that a quote taken IN such a transcript goes to the nearest writable
 * agent instead of into the composer beside it.
 *
 * This is the tab-state answer, not the last word: `EditorRef.writable` is what
 * actually refuses a write, because the mounted editor is the one that knows and
 * every route to a composer goes through that registry.
 */
export function isSteerableAgentTab(tab: { type: TabType, parentAgentId?: string, acceptsMessages?: boolean, agentProvider?: AgentProvider }): boolean {
  if (tab.type !== TabType.AGENT)
    return false
  if (!tab.parentAgentId)
    return true
  // acceptsMessages (backend-authoritative) wins when present. Before
  // hydration, fall back to the provider plugin's supportsSubagentSend so
  // "which providers can steer a subagent" has a single source of truth (the
  // Codex plugin sets it true; all others omit it). Adding a second steerable
  // provider then needs no edit here.
  if (tab.acceptsMessages !== undefined)
    return tab.acceptsMessages
  return pluginFor(tab.agentProvider)?.supportsSubagentSend ?? false
}

/**
 * Find the most-recent agent tab that is STEERABLE. Used by callers that must
 * target a real (writable) agent for a mention/quote insert or a working-
 * directory lookup -- a non-steerable child (a read-only subagent transcript)
 * must never be selected. `tabs` is assumed already in MRU order.
 */
export function mruSteerableAgentTab<T extends Tab>(tabs: readonly T[]): T | undefined {
  return tabs.find(t => t.type === TabType.AGENT && isSteerableAgentTab(t))
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
    gitStatus: undefined,
    createdAt: tab.createdAt ?? '',
    closedAt: '',
    homeDir: '',
    startupError: tab.startupError ?? '',
    startupMessage: tab.startupMessage ?? '',
    parentAgentId: tab.parentAgentId ?? '',
    acceptsMessages: tab.acceptsMessages ?? false,
    rootAgentId: tab.rootAgentId ?? '',
  } as AgentInfo
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
 * Optimistic `gitToplevel` to seed on a freshly-opened tab of ANY kind --
 * agent, terminal, or file. Branch/origin/worktree live in the repo-keyed git
 * store; only the toplevel identity travels on the tab row. The branch copy
 * that goes with it is {@link planOptimisticRepoGit}, which now owns both
 * halves and defers the store write behind tab placement -- do not write the
 * entry directly beside this.
 *
 * Only safe to seed when the active tab and the new tab resolve to the same
 * git directory — otherwise the seeded value would be wrong for the new tab's
 * repo.
 *
 * ANY tab kind may be the SOURCE. A tab with no toplevel has nothing to seed
 * and returns early; one in a different directory fails the dir match.
 *
 * ABSENT KEYS ARE OMITTED, not set to `undefined`. Object spread copies
 * `undefined`-valued OWN keys, so a key present-but-undefined does not "leave
 * the value alone", it ERASES it.
 */
export function resolveOptimisticGitInfo(
  activeTab: Tab | null | undefined,
  newTab: { shellStartDir?: string, workingDir?: string },
): { gitToplevel?: string } {
  if (!activeTab)
    return {}
  if (!activeTab.gitToplevel)
    return {}
  const activeDir = effectiveGitDir(activeTab)
  const newDir = effectiveGitDir(newTab)
  if (!activeDir || activeDir !== newDir)
    return {}
  return { gitToplevel: activeTab.gitToplevel }
}

/** What {@link planOptimisticRepoGit} hands back to an open path. */
export interface OptimisticRepoGitPlan {
  /** Row fields for the new tab. Empty when the directories do not match. */
  fields: { gitToplevel?: string }
  /**
   * Write the store copy. Call this ONLY once the tab exists.
   *
   * Placement can still be refused, and a caller that wrote the entry first
   * left one behind that no tab reads. Nothing reclaims such an entry, and
   * `hasHealthyRepoForProbe` then treats it as a real repo for one probe.
   */
  commit: () => void
}

/**
 * Plan the optimistic copy of branch/origin/worktree from the active tab's repo
 * entry onto the new tab's key, for the case where both resolve to the same git
 * directory. Same-worker opens share a key and need no copy; different worker
 * ids need this to avoid a sidebar flash with no branch name.
 *
 * ONE evaluation feeds both halves. The row fields and the store copy used to
 * be resolved by two calls, from two hand-built argument objects that had to
 * agree on `shellStartDir` -- and a caller that built them differently decided
 * the directory match against one tab and copied the branch from another.
 *
 * The copy is DEFERRED rather than done here, so an open that fails to place
 * its tab writes nothing. See {@link OptimisticRepoGitPlan.commit}.
 */
export function planOptimisticRepoGit(
  store: Pick<RepoGitStore, 'get' | 'upsert'>,
  activeTab: Tab | null | undefined,
  newTab: { workerId?: string, shellStartDir?: string, workingDir?: string },
): OptimisticRepoGitPlan {
  const fields = resolveOptimisticGitInfo(activeTab, newTab)
  const noop = { fields, commit: () => {} }

  const workerId = newTab.workerId ?? ''
  if (!fields.gitToplevel || !workerId || !activeTab?.workerId)
    return noop
  const fromKey = repoKeyFromTab(activeTab)
  if (!fromKey)
    return noop
  const toKey = repoKey(workerId, fields.gitToplevel)
  if (toKey === fromKey)
    return noop

  const toplevel = fields.gitToplevel
  return {
    fields,
    commit: () => {
      // Read at COMMIT time, not at plan time. The worker's own answer can
      // land between the two, and it must win.
      const from = store.get(fromKey)
      if (!from?.toplevel)
        return
      const existing = store.get(toKey)
      // `gitStatusSeen` is the difference between "nothing probed this key
      // yet" and "the worker answered, and this repo genuinely has no branch"
      // -- a detached HEAD, or a non-repo stub. Both leave `branch` and
      // `originUrl` empty, so the emptiness alone cannot tell them apart, and
      // a guess would overwrite the worker's own answer with another
      // machine's branch.
      if (existing?.gitStatusSeen || existing?.originUrl || existing?.branch)
        return
      store.upsert(toKey, {
        workerId,
        toplevel,
        branch: from.branch,
        originUrl: from.originUrl,
        isWorktree: from.isWorktree,
        gitStatusSeen: from.gitStatusSeen,
      })
    },
  }
}

/**
 * Tab fields for an agent this client just opened, from the `AgentInfo` that
 * the OpenAgent response carried. The sibling of {@link openedTerminalMetadata}.
 *
 * `hydrated: true` belongs to the helper, not to the call site. The OpenAgent
 * response IS the worker's answer for this tab, so `useTabHydrators` must not
 * re-ask. Its reply arrives without the pending-axis suppression that the live
 * settings path applies, and it overwrites an optimistic settings edit made
 * during the round trip. Every local-open path needs the flag, so a fourth one
 * cannot ship without it.
 *
 * This helper writes NO repo-keyed git entry, and it needs none. The response
 * carries no git status: the worker computes the status in startup phase 1 and
 * sends it on the STARTING broadcast, because the `git status` shell-out would
 * otherwise block the RPC. `TestOpenAgent_ResponseHasNilGitStatus` holds the
 * worker to that. `protoToAgentTabFields` therefore writes no `gitToplevel`
 * either, so the row and the store stay consistent -- both empty -- until
 * `applyAgentStatusTabUpdate` writes the pair from the broadcast.
 */
export function openedAgentTabFields(
  store: Pick<RepoGitStore, 'get' | 'upsert' | 'clear'>,
  agent: AgentInfo,
) {
  // No orphan-migration tip: a local open has no live tab to compute one from.
  //
  // The return type is inferred, like `openedTerminalMetadata`'s: `hydrated`
  // lives on `TabMetadata`, not on `AgentTab`, so `Partial<AgentTab>` would
  // drop it.
  return { ...protoToAgentTabFields(store, agent.workerId, agent), hydrated: true }
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
  // An IMAGE tab's name comes from its payload, which the tab strip reads as
  // `title` above; this is the pre-hydration placeholder.
  if (tab.type === TabType.IMAGE)
    return 'Image'
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

/**
 * When a tab's tooltip may hide behind clip detection.
 *
 * `showWhen="clipped"` is for a tooltip that REPEATS its label -- that is the
 * mode's contract, and it also withholds the text from a screen reader. A
 * terminal's tooltip carries its live PTY title, which the label never shows
 * (the label is the worker-assigned name or a user rename), so gating that on
 * the label happening to overflow hides the OSC title outright.
 *
 * Every surface that pairs {@link tabTooltipText} with {@link tabDisplayLabel}
 * needs this, so the rule lives here rather than as a ternary at each one.
 */
export function tabTooltipShowWhen(tab: Tab): 'always' | 'clipped' {
  return tabTooltipText(tab) === tabDisplayLabel(tab) ? 'clipped' : 'always'
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

/**
 * Whether a tab may be closed from a surface that renders `archived`'s
 * workspace.
 *
 * An archived workspace takes no mutation, so no tab type keeps a close
 * control.
 *
 * Shared by the tab bar and the sidebar tree, which render the same tabs and
 * must not disagree about which of them close.
 */
export function canCloseTab(archived: boolean | undefined, _tab: Tab): boolean {
  return !archived
}

/**
 * Whether a tab may be RENAMED from a surface that renders `archived`'s
 * workspace.
 *
 * A payload-backed tab takes its title from what it SHOWS -- a file's path, or
 * the name the image arrived under -- so a user-typed name has nothing to hold.
 * An archived workspace takes no mutation at all.
 *
 * The twin of {@link canCloseTab}, and shared for the same reason: the tab bar
 * and the sidebar tree render the same tabs and must not disagree about which of
 * them rename. Each surface still ANDs in whether it HAS a rename handler, which
 * is a fact about that surface rather than about the tab.
 */
export function canRenameTab(archived: boolean | undefined, tab: Tab): boolean {
  return !archived && !isPayloadBackedTabType(tab.type)
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

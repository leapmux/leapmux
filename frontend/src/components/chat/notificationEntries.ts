import type { CompactionDetails, NotificationEntry, NotificationIconHint, SettingChange } from './model/notification'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { GoalStatus } from '~/stores/chatGoal'
import { GOAL_TRANSITION, NOTIFICATION_FIELD, NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { isObject, pickString } from '~/lib/jsonPick'
import { messagesOf } from '~/lib/messageParser'
import { formatRateLimitMessage } from '~/lib/rateLimitUtils'
import { getCachedSettingsGroupLabel, getCachedSettingsLabel } from '~/lib/settingsLabelCache'
import { backgroundTaskStatusFromWire } from '~/stores/chatBackgroundTasks'
import { goalStatusFromWire } from '~/stores/chatGoal'
import { pluginFor } from './providers/registry'
import { formatShortWait, formatTokenCount } from './rendererUtils'
import { OPTION_ID_PERMISSION_MODE } from './settingsGroups'

// The notification pipeline, without a line of markup.
//
// Three steps, in order: DISPATCH one message to whoever can read it, FLATTEN the
// structured entries it returns into the blocks a row lays out, and let
// `notificationRenderers.tsx` draw those blocks. Splitting the flatten from the draw
// is what keeps every decision about WHAT a row says out of the markup, so a reader
// of either half sees one of the two concerns and not both.

/** What survives flattening: the two things a notification row lays out. */
export type NotificationBlock
  = | { kind: 'text', text: string }
    | { kind: 'subagent-report', label?: string, text: string, status?: string }
    | { kind: 'divider', text: string, loading?: boolean, icon?: NotificationIconHint }

// Provider-neutral notification labels. Named constants so the wording lives in one
// place and every reader refers to it by name.
const CONTEXT_CLEARED_LABEL = 'Context cleared'
const INTERRUPTED_LABEL = 'Interrupted'
// The instruction matters as much as the fact: the second Stop press is the one the
// worker escalates into a forced stop, and the row is where the reader learns that.
const STOP_IGNORED_LABEL = 'Stop ignored — press Stop again to force it'
const UNKNOWN_ERROR_LABEL = 'Unknown error'
export const COMPACTING_LABEL = 'Compacting context...'
// Claude Code emits no metadata for a microcompaction, so this label carries no
// detail -- and it must stay distinct, because a micro pass is not a full one.
const MICROCOMPACT_LABEL = 'Context microcompacted'

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

/**
 * The entries one notification message produces.
 *
 * A message whose `type` the worker writes goes to ONE shared extractor and never
 * reaches a plugin: the worker writes those rows and no agent emits them, so a plugin
 * that recognized one by accident would shadow the neutral reading and nothing would
 * fail. This is the rule `classifyMessage` already applies to the same set.
 *
 * Everything else is the provider's own frame, so the provider reads it. There is NO
 * shared fallback below the plugin: a shared switch that answered last carried Claude
 * and Codex wire shapes for years, which is the layering this refactor removes.
 */
export function notificationEntriesFor(
  message: Record<string, unknown>,
  agentProvider: AgentProvider | undefined,
): NotificationEntry[] {
  return leapmuxNotificationEntry(message, agentProvider)
    ?? pluginFor(agentProvider)?.transcript.notificationEntry?.(message)
    ?? []
}

/**
 * The entries a WORKER-authored notification produces.
 *
 * Every branch reads LeapMux's own envelope, never a provider's. Adding a type is one
 * edit here plus its `worker-vocab.json` entry; the per-provider tables it replaced
 * each had to learn it, and the one that forgot drew the row as raw JSON.
 */
export function leapmuxNotificationEntry(
  m: Record<string, unknown>,
  agentProvider: AgentProvider | undefined,
): NotificationEntry[] | null {
  switch (m.type) {
    case NOTIFICATION_TYPE.SettingsChanged: {
      const changes = parseSettingsChanges(m[NOTIFICATION_FIELD.Changes], agentProvider)
      return changes.length > 0 ? [{ kind: 'settings-changed', changes }] : []
    }
    case NOTIFICATION_TYPE.ContextCleared:
      return [{ kind: 'context-cleared' }]
    // The START of a compaction, in LeapMux's own envelope. It carries no size and no
    // trigger, because the row states only that the agent began to rewrite its
    // context. The neutral answer is the SAME entry Claude, Codex, Pi and Copilot each
    // build from their own start frame, so it invents no wording: `blocksForEntry`
    // owns the one label and this branch chooses no words of its own.
    case NOTIFICATION_TYPE.Compacting:
      return [{ kind: 'compaction', phase: 'start' }]
    case NOTIFICATION_TYPE.PlanExecution:
      return [{ kind: 'text', text: 'Executing plan' }]
    case NOTIFICATION_TYPE.AgentError:
      return [{ kind: 'text', text: pickString(m, NOTIFICATION_FIELD.Error, UNKNOWN_ERROR_LABEL) }]
    case NOTIFICATION_TYPE.Interrupted:
      return [{ kind: 'text', text: INTERRUPTED_LABEL }]
    case NOTIFICATION_TYPE.StopIgnored:
      return [{ kind: 'text', text: STOP_IGNORED_LABEL }]
    // A live status the provider reported in its own words. The worker
    // normalized it, so one row draws every provider's.
    case NOTIFICATION_TYPE.AgentStatus: {
      const text = pickString(m, NOTIFICATION_FIELD.Text).trim()
      return text ? [{ kind: 'status', text }] : []
    }
    case NOTIFICATION_TYPE.SubagentEnded:
      return [subagentEndedEntry(m)]
    case NOTIFICATION_TYPE.SubagentReport: {
      const text = pickString(m, NOTIFICATION_FIELD.Text).trim()
      if (!text)
        return []
      const label = pickString(m, NOTIFICATION_FIELD.Label).trim()
      const status = pickString(m, NOTIFICATION_FIELD.Status).trim()
      return [{ kind: 'subagent-report', text, ...(label ? { label } : {}), ...(status ? { status } : {}) }]
    }
    case NOTIFICATION_TYPE.PlanUpdated: {
      const label = planUpdatedLabel(m)
      return label !== null ? [{ kind: 'text', text: label }] : []
    }
    case NOTIFICATION_TYPE.GoalUpdated: {
      const label = goalUpdatedLabel(m)
      return label !== null ? [{ kind: 'text', text: label }] : []
    }
    case NOTIFICATION_TYPE.GoalCleared: {
      const objective = pickString(m, NOTIFICATION_FIELD.Objective)
      return [{ kind: 'text', text: objective ? `Goal cleared: ${objective}` : 'Goal cleared' }]
    }
    // Not LeapMux's own envelope. The provider reads it.
    default:
      return null
  }
}

// ---------------------------------------------------------------------------
// Worker-authored label helpers
// ---------------------------------------------------------------------------

function displayLabel(provider: AgentProvider | undefined, key: string): string {
  // Prefer the per-provider cached group label so a provider that relabels a well-known
  // axis is honored (Pi labels its effort axis "Thinking Level"), then fall back to the
  // canonical English name. The well-known fallbacks keep a historical notification
  // readable when the cache hasn't been primed for that provider yet.
  return getCachedSettingsGroupLabel(provider, key) ?? wellKnownAxisLabel(key)
}

function wellKnownAxisLabel(key: string): string {
  switch (key) {
    case 'model': return 'Model'
    case 'effort': return 'Effort'
    case OPTION_ID_PERMISSION_MODE: return 'Permission Mode'
    default: return key
  }
}

function displayValue(provider: AgentProvider | undefined, key: string, value: string): string {
  return getCachedSettingsLabel(provider, key, value) ?? value
}

/**
 * Read the raw, untyped `changes` map into resolved changes.
 *
 * Each entry is expected to be `{ old, new, label?, old_label?, new_label? }`: inline
 * overrides win over the cache-derived display, an unchanged entry is dropped, and a
 * non-object entry is skipped so a malformed payload degrades rather than throws.
 */
export function parseSettingsChanges(changes: unknown, provider: AgentProvider | undefined): SettingChange[] {
  if (!isObject(changes))
    return []
  const result: SettingChange[] = []
  for (const [key, val] of Object.entries(changes)) {
    if (!isObject(val))
      continue
    const oldValue = pickString(val, NOTIFICATION_FIELD.Old)
    const newValue = pickString(val, NOTIFICATION_FIELD.New)
    if (oldValue === newValue)
      continue
    // The "(new)"-only form depends on the absence of an old VALUE, not on an
    // empty old DISPLAY: a real value whose display resolves to "" is still a
    // transition and must keep the arrow.
    const old = oldValue === '' ? undefined : pickString(val, NOTIFICATION_FIELD.OldLabel, undefined) ?? displayValue(provider, key, oldValue)
    result.push({
      // `?? ` (not `||`) so an explicit empty-string override is honored rather
      // than silently falling back to the cache-derived display.
      label: pickString(val, NOTIFICATION_FIELD.Label, undefined) ?? displayLabel(provider, key),
      ...(old !== undefined ? { old } : {}),
      new: pickString(val, NOTIFICATION_FIELD.NewLabel, undefined) ?? displayValue(provider, key, newValue),
    })
  }
  return result
}

/**
 * The plan_updated label, or null when there is no title to show. Two variants:
 * "Plan updated and renamed to <title>" when `update_agent_title` is set, else
 * "Plan updated: <title>".
 */
function planUpdatedLabel(source: Record<string, unknown>): string | null {
  const title = pickString(source, NOTIFICATION_FIELD.PlanTitle)
  if (!title)
    return null
  return source[NOTIFICATION_FIELD.UpdateAgentTitle] === true
    ? `Plan updated and renamed to ${title}`
    : `Plan updated: ${title}`
}

/** What each transition DID, in the worker's own vocabulary. */
const GOAL_TRANSITION_VERBS: Partial<Record<string, string>> = {
  [GOAL_TRANSITION.Set]: 'Goal set',
  [GOAL_TRANSITION.Replaced]: 'Goal replaced',
  [GOAL_TRANSITION.Resumed]: 'Goal resumed',
  [GOAL_TRANSITION.Paused]: 'Goal paused',
  [GOAL_TRANSITION.Blocked]: 'Goal blocked',
  [GOAL_TRANSITION.Achieved]: 'Goal achieved',
}

/** The fallback verb, from the resulting status alone. */
const GOAL_STATUS_VERBS: Record<GoalStatus, string> = {
  active: 'Goal set',
  paused: 'Goal paused',
  blocked: 'Goal blocked',
  done: 'Goal achieved',
  dormant: 'Goal paused',
}

/**
 * The transcript label for a session-goal transition.
 *
 * The worker writes these rows only when the goal actually CHANGES -- never for the
 * progress reports Codex sends after every completed tool call -- so each one is worth
 * a line. The row is provider-NEUTRAL: five CLIs report a goal in five wire shapes and
 * the worker normalizes them.
 */
function goalUpdatedLabel(source: Record<string, unknown>): string | null {
  const objective = pickString(source, NOTIFICATION_FIELD.Objective)
  if (!objective)
    return null
  const status = pickString(source, NOTIFICATION_FIELD.GoalStatus)
  // The verb comes from what the change DID, not from the state it left behind.
  // Several changes end in one status -- a resume and a first set both end `active` --
  // and only the worker, which holds the row from before the write, can tell them
  // apart, so it writes the answer here.
  //
  // `Object.hasOwn`, not a bare index. The token comes off a persisted payload, and a
  // plain-object lookup answers `Object.prototype` for `__proto__` and a function for
  // `constructor` -- both truthy, so `??` would not fall through and the row would
  // render "[object Object]: <objective>".
  const transition = pickString(source, NOTIFICATION_FIELD.GoalTransition)
  const transitionVerb = transition && Object.hasOwn(GOAL_TRANSITION_VERBS, transition)
    ? GOAL_TRANSITION_VERBS[transition]
    : undefined
  const verb = transitionVerb ?? GOAL_STATUS_VERBS[goalStatusFromWire(status) ?? 'active']
  // The provider's own word, when it says more than the neutral status does --
  // "usageLimited" and "notSatisfied" are both `blocked`.
  const detail = pickString(source, NOTIFICATION_FIELD.StatusDetail)
  const suffix = detail && detail !== status ? ` (${detail})` : ''
  return `${verb}: ${objective}${suffix}`
}

/**
 * Label + glyph for the divider that closes a subagent transcript.
 *
 * The glyph is this divider's own. The Background tasks list carries the same states
 * as a COLOUR on one constant dot rather than as a glyph per state, so there is no
 * shared glyph vocabulary to match; the shared reading comes from the label, which
 * lists the same four outcomes.
 */
function subagentEndedEntry(m: Record<string, unknown>): NotificationEntry {
  // Narrowed through the store's wire reader, so the four final statuses are spelled
  // out in one place rather than re-listed here.
  switch (backgroundTaskStatusFromWire(pickString(m, NOTIFICATION_FIELD.Status) ?? '')) {
    case 'completed':
      return { kind: 'divider', text: 'Subagent completed', icon: 'succeeded' }
    case 'failed':
      return { kind: 'divider', text: 'Subagent failed', icon: 'failed' }
    case 'stopped':
      return { kind: 'divider', text: 'Subagent stopped', icon: 'stopped' }
    case 'interrupted':
      return { kind: 'divider', text: 'Subagent interrupted', icon: 'interrupted' }
    default:
      // An unknown final status still ends the transcript; say only what is certain
      // rather than inventing an outcome.
      return { kind: 'divider', text: 'Subagent ended', icon: 'stopped' }
  }
}

// ---------------------------------------------------------------------------
// Formatting: one structured entry becomes one block
// ---------------------------------------------------------------------------

/**
 * Format a pre/post token pair as the transition "105.4k → 8.5k", degrading to
 * "105.4k" (pre only), "→ 8.5k" (post only), or "" when neither is known.
 */
function formatTokenTransition(pre: number | undefined, post: number | undefined): string {
  if (typeof pre === 'number' && typeof post === 'number')
    return `${formatTokenCount(pre)} → ${formatTokenCount(post)}`
  if (typeof pre === 'number')
    return formatTokenCount(pre)
  if (typeof post === 'number')
    return `→ ${formatTokenCount(post)}`
  return ''
}

/**
 * The parenthetical detail of a compaction, " (manual, 105.4k → 8.5k)".
 *
 * Every part is optional, so the result can be " (manual)", " (manual, 105.4k)",
 * " (105.4k → 8.5k)", " (→ 8.5k)", or "" when nothing is known.
 */
function formatCompactionDetail(detail: CompactionDetails | undefined): string {
  if (!detail)
    return ''
  const tokens = formatTokenTransition(detail.pre, detail.post)
  const parts = [detail.trigger, tokens].filter(Boolean)
  return parts.length > 0 ? ` (${parts.join(', ')})` : ''
}

/** "Context compacted" plus the formatted token detail. */
export function compactedLabel(detail: CompactionDetails | undefined): string {
  return `Context compacted${formatCompactionDetail(detail)}`
}

/** One settings change as `Label (old → new)`, or `Label (new)` when there was none. */
function formatSettingChange(change: SettingChange): string {
  return change.old === undefined
    ? `${change.label} (${change.new})`
    : `${change.label} (${change.old} → ${change.new})`
}

/**
 * The retry sentence: what the agent retries, which attempt it is on, how long it
 * waits, and what went wrong.
 *
 * ONE wording for every provider. Claude wrote "API Retry 1/3 (529 overloaded)" and Pi
 * wrote "Auto-retry 1/3 in 2s…" for the same stall, and a reader who moved between
 * them had to learn both.
 */
function formatRetry(entry: Extract<NotificationEntry, { kind: 'retry' }>): string {
  const what = entry.scope === 'summarization' ? 'Summary retry' : 'API retry'
  const count = entry.attempt !== undefined
    ? ` ${entry.attempt}${entry.maxAttempts !== undefined ? `/${entry.maxAttempts}` : ''}`
    : ''
  const detail = entry.error?.trim()
  const suffix = detail ? ` (${detail})` : ''
  if (entry.succeeded)
    return `${what}${count} succeeded`
  if (entry.willRetry === false)
    return `${what}${count} gave up${suffix}`
  const wait = entry.delayMs !== undefined && entry.delayMs > 0 ? ` in ${formatShortWait(entry.delayMs)}` : ''
  return `${what}${count}${wait}${suffix}`
}

/**
 * Flatten one structured entry into the blocks a row lays out.
 *
 * A `group` entry never reaches here: {@link flattenNotificationEntries} coalesces a
 * run of them into one text block first.
 */
function blocksForEntry(entry: Exclude<NotificationEntry, { kind: 'group' }>): NotificationBlock[] {
  switch (entry.kind) {
    case 'text':
      return [{ kind: 'text', text: entry.text }]
    case 'subagent-report':
      return [{
        kind: 'subagent-report',
        text: entry.text,
        ...(entry.label ? { label: entry.label } : {}),
        ...(entry.status ? { status: entry.status } : {}),
      }]
    case 'divider':
      return [{
        kind: 'divider',
        text: entry.text,
        ...(entry.loading !== undefined ? { loading: entry.loading } : {}),
        ...(entry.icon !== undefined ? { icon: entry.icon } : {}),
      }]
    case 'rate-limit':
      return entry.tiers.map(tier => ({ kind: 'text' as const, text: formatRateLimitMessage(tier) }))
    case 'settings-changed': {
      const text = entry.changes.map(formatSettingChange).join(', ')
      return text ? [{ kind: 'text', text }] : []
    }
    case 'retry':
      return [{ kind: 'text', text: formatRetry(entry) }]
    case 'compaction': {
      if (entry.phase === 'start')
        return [{ kind: 'divider', text: COMPACTING_LABEL, loading: true }]
      // A compaction that ABORTED or failed left no boundary behind, so it draws a
      // plain line rather than the rule that marks where the context was rewritten.
      const error = entry.error?.trim()
      if (error)
        return [{ kind: 'text', text: error === 'aborted' ? 'Context compaction aborted' : `Compaction failed (${error})` }]
      return [{ kind: 'divider', text: entry.micro ? MICROCOMPACT_LABEL : compactedLabel(entry.detail) }]
    }
    case 'context-cleared':
      return [{ kind: 'text', text: CONTEXT_CLEARED_LABEL }]
    case 'status':
      return entry.text ? [{ kind: 'text', text: entry.text }] : []
  }
}

/**
 * Flatten every entry of a thread into ordered blocks, coalescing grouped runs.
 *
 * A run of `group` entries that share a `groupKey` collapses into one
 * `Prefix: a, b, c` block. The run ends at the first entry of any other kind, so the
 * order a provider produced is the order a reader sees.
 */
export function flattenNotificationEntries(entries: readonly NotificationEntry[]): NotificationBlock[] {
  const blocks: NotificationBlock[] = []
  const groupOrder: string[] = []
  const groups = new Map<string, { prefix: string, entries: string[] }>()

  const flushGroups = () => {
    if (groupOrder.length === 0)
      return
    for (const key of groupOrder) {
      const group = groups.get(key)
      if (!group || group.entries.length === 0)
        continue
      blocks.push({ kind: 'text', text: `${group.prefix}: ${group.entries.join(', ')}` })
    }
    groups.clear()
    groupOrder.length = 0
  }

  for (const entry of entries) {
    if (entry.kind === 'group') {
      const existing = groups.get(entry.groupKey)
      if (existing) {
        existing.entries.push(entry.entry)
      }
      else {
        groups.set(entry.groupKey, { prefix: entry.prefix, entries: [entry.entry] })
        groupOrder.push(entry.groupKey)
      }
      continue
    }
    flushGroups()
    blocks.push(...blocksForEntry(entry))
  }

  flushGroups()
  return blocks
}

/**
 * The post-compaction context size a notification states, for refreshing the
 * context-usage grid the instant a boundary lands -- rather than leaving the now-stale
 * pre-compaction usage on screen until the next assistant message overwrites it.
 *
 * Scans a consolidated wrapper in REVERSE so the most recent boundary wins, and skips
 * a boundary that carries no resolvable post (a Codex item with no metadata is the
 * live case) so an earlier one that does can still refresh the grid.
 *
 * Provider-dispatched: which frame is a boundary is the provider's question, and
 * `compactionBoundaryFromMessage` answers it. The shape test used to live in
 * `messageParser`, where it carried Claude's and Codex's wire shapes side by side.
 */
export function compactionContextTokens(parsed: ParsedMessageContent, agentProvider?: AgentProvider): number | undefined {
  const boundary = pluginFor(agentProvider)?.session?.compactionBoundaryFromMessage
  if (!boundary)
    return undefined
  const messages = messagesOf(parsed)
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i]
    if (!isObject(msg))
      continue
    // Each entry of a wrapper is asked on its own, as a one-message parse: the hook
    // takes a parsed message, and a wrapper's entry is exactly that.
    const post = boundary({ ...parsed, wrapper: null, topLevel: msg, parentObject: msg })?.post
    if (post !== undefined)
      return post
  }
  return undefined
}

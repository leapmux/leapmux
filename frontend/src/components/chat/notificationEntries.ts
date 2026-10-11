import type { CompactionDetails, NotificationEntry, SettingChange } from './model/notification'
import type { GoalTransitionToken } from '~/generated/contracts/worker-vocab'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { GoalStatus } from '~/stores/chatGoal'
import { GOAL_TRANSITION, NOTIFICATION_FIELD, NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { isObject, pickString } from '~/lib/jsonPick'
import { messagesOf } from '~/lib/messageParser'
import { formatRateLimitMessage } from '~/lib/rateLimitUtils'
import { getCachedSettingsGroupLabel, getCachedSettingsLabel } from '~/lib/settingsLabelCache'
import { goalStatusFromWire } from '~/stores/chatGoal'
import { pluginFor } from './providers/registry'
import { formatShortWait, formatTokenCount } from './rendererUtils'
import { OPTION_ID_PERMISSION_MODE } from './settingsGroups'

// Read notification messages without rendering markup.
//
// The pipeline has three steps:
// - Select the reader for each message.
// - Flatten its structured entries into notification blocks.
// - Let notificationRenderers.tsx draw those blocks.
//
// The reader decides what a row says. The renderer decides how to display it.

/** The blocks that a notification row can display. */
export type NotificationBlock
  = | { kind: 'text', text: string }
    | { kind: 'subagent-report', label?: string, text: string, status?: string }
    | { kind: 'divider', text: string, loading?: boolean }

// Shared notification labels keep each message's wording in one place.
const CONTEXT_CLEARED_LABEL = 'Context cleared'
const INTERRUPTED_LABEL = 'Interrupted'
// The second Interrupt press requests a forced stop.
// The row explains that control to the user.
const INTERRUPT_IGNORED_LABEL = 'Interrupt ignored — press Interrupt again to force it'
// The worker queues the message and writes this row independently.
// The queue can deliver the message before the row arrives.
const INPUT_REQUEUED_LABEL = 'Message queued again — the agent dropped it before the model read it'
const UNKNOWN_ERROR_LABEL = 'Unknown error'
export const COMPACTING_LABEL = 'Compacting context...'
// Claude Code reports no microcompaction metadata.
// Keep its label separate from the full compaction label.
const MICROCOMPACT_LABEL = 'Context microcompacted'

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

/**
 * Read the entries from one notification message.
 *
 * The shared extractor reads a worker notification before any provider reader.
 * A provider reader must not change the meaning of a worker notification.
 * classifyMessage applies the same distinction to these message types.
 *
 * The provider reader reads other frames.
 * There is no shared provider fallback after that reader.
 */
export function notificationEntriesFor(
  message: Record<string, unknown>,
  agentProvider: AgentProvider | undefined,
): NotificationEntry[] {
  return notificationEntriesForReader(
    message,
    agentProvider,
    pluginFor(agentProvider)?.transcript.notificationEntry,
  )
}

export function notificationEntriesForReader(
  message: Record<string, unknown>,
  agentProvider: AgentProvider | undefined,
  providerReader?: (message: Record<string, unknown>) => NotificationEntry[],
): NotificationEntry[] {
  return leapmuxNotificationEntry(message, agentProvider) ?? providerReader?.(message) ?? []
}

/**
 * Read the entries from a worker notification.
 * Each case reads the worker envelope rather than a provider envelope.
 * A new notification type requires its generated worker vocabulary and this extractor's case.
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
    // The worker envelope reports that compaction started.
    // It supplies no trigger or token count.
    // blocksForEntry gives this neutral start entry its shared label.
    case NOTIFICATION_TYPE.Compacting:
      return [{ kind: 'compaction', phase: 'start' }]
    case NOTIFICATION_TYPE.PlanExecution:
      return [{ kind: 'text', text: 'Executing plan' }]
    case NOTIFICATION_TYPE.AgentError:
      return [{ kind: 'text', text: pickString(m, NOTIFICATION_FIELD.Error, UNKNOWN_ERROR_LABEL) }]
    case NOTIFICATION_TYPE.Interrupted:
      return [{ kind: 'text', text: INTERRUPTED_LABEL }]
    case NOTIFICATION_TYPE.StopIgnored:
      return [{ kind: 'text', text: INTERRUPT_IGNORED_LABEL }]
    case NOTIFICATION_TYPE.InputRequeued:
      return [{ kind: 'text', text: INPUT_REQUEUED_LABEL }]
    // The worker normalizes the provider's live status text.
    case NOTIFICATION_TYPE.AgentStatus: {
      const text = pickString(m, NOTIFICATION_FIELD.Text).trim()
      return text ? [{ kind: 'status', text }] : []
    }
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
    // The provider reader handles this message type.
    default:
      return null
  }
}

// Worker notification labels.

function displayLabel(provider: AgentProvider | undefined, key: string): string {
  // Use the provider's cached group label before the canonical English label.
  // For example, Pi displays its effort group as "Thinking Level".
  // The canonical label keeps a notification readable when the cache has no entry.
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
 * Read an untyped changes map into resolved setting changes.
 * Each entry can contain old and new values with optional display labels.
 * Inline labels take precedence over cached labels.
 * Skip unchanged entries and entries that are not objects.
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
    // The new-only form requires an absent old value.
    // An empty old display label still belongs to a real transition.
    const old = oldValue === '' ? undefined : pickString(val, NOTIFICATION_FIELD.OldLabel, undefined) ?? displayValue(provider, key, oldValue)
    result.push({
      // Nullish coalescing preserves an explicit empty display label.
      // A truthy fallback would replace that label with a cached value.
      label: pickString(val, NOTIFICATION_FIELD.Label, undefined) ?? displayLabel(provider, key),
      ...(old !== undefined ? { old } : {}),
      new: pickString(val, NOTIFICATION_FIELD.NewLabel, undefined) ?? displayValue(provider, key, newValue),
    })
  }
  return result
}

/**
 * Return the plan_updated label, or null when the title is empty.
 * Use the renamed label only when update_agent_title is true.
 */
function planUpdatedLabel(source: Record<string, unknown>): string | null {
  const title = pickString(source, NOTIFICATION_FIELD.PlanTitle)
  if (!title)
    return null
  return source[NOTIFICATION_FIELD.UpdateAgentTitle] === true
    ? `Plan updated and renamed to ${title}`
    : `Plan updated: ${title}`
}

/** The label for each generated goal transition. */
const GOAL_TRANSITION_VERBS: Partial<Record<string, string>> = {
  [GOAL_TRANSITION.Set]: 'Goal set',
  [GOAL_TRANSITION.Replaced]: 'Goal replaced',
  [GOAL_TRANSITION.Updated]: 'Goal updated',
  [GOAL_TRANSITION.Resumed]: 'Goal resumed',
  [GOAL_TRANSITION.Paused]: 'Goal paused',
  [GOAL_TRANSITION.Blocked]: 'Goal blocked',
  [GOAL_TRANSITION.Achieved]: 'Goal achieved',
} satisfies Record<GoalTransitionToken, string>

/** The fallback label for each resulting goal status. */
const GOAL_STATUS_VERBS: Record<GoalStatus, string> = {
  active: 'Goal set',
  paused: 'Goal paused',
  blocked: 'Goal blocked',
  done: 'Goal achieved',
  dormant: 'Goal paused',
  unknown: 'Goal status unknown',
}

/**
 * Return the transcript label for a session-goal transition.
 * The worker writes a goal_updated row when the durable goal changes.
 * Progress-only reports use session info and create no goal_updated row.
 * The shared worker envelope lets this reader serve every provider.
 */
function goalUpdatedLabel(source: Record<string, unknown>): string | null {
  const objective = pickString(source, NOTIFICATION_FIELD.Objective)
  if (!objective)
    return null
  const status = pickString(source, NOTIFICATION_FIELD.GoalStatus)
  // Prefer the reported transition over the resulting status.
  // Set and Resume can both end in active, so the status cannot distinguish them.
  //
  // Use Object.hasOwn before the lookup because the payload supplies the token.
  // An unguarded lookup could accept inherited __proto__ or constructor values.
  // Those values would prevent the fallback and produce an invalid label.
  const transition = pickString(source, NOTIFICATION_FIELD.GoalTransition)
  const transitionVerb = transition && Object.hasOwn(GOAL_TRANSITION_VERBS, transition)
    ? GOAL_TRANSITION_VERBS[transition]
    : undefined
  const recognizedStatus = goalStatusFromWire(status)
  const verb = transitionVerb ?? GOAL_STATUS_VERBS[recognizedStatus ?? 'unknown']
  // Skip detail that repeats a recognized status token.
  // Keep matching native detail when the status token is unrecognized.
  // The neutral fallback does not display that native word.
  const detail = pickString(source, NOTIFICATION_FIELD.StatusDetail)
  const suffix = detail && (recognizedStatus === undefined || detail !== status) ? ` (${detail})` : ''
  return `${verb}: ${objective}${suffix}`
}

// ---------------------------------------------------------------------------
// Formatting: one structured entry becomes one block
// ---------------------------------------------------------------------------

/**
 * Format both reported token counts as "105.4k → 8.5k".
 * A single reported count displays only its side of that transition.
 * Two absent counts produce an empty string.
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
 * Format the optional compaction trigger and token counts in parentheses.
 * An absent trigger or token count stays absent.
 * No reported detail produces an empty string.
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

/** Format one settings change with its optional old display value. */
function formatSettingChange(change: SettingChange): string {
  return change.old === undefined
    ? `${change.label} (${change.new})`
    : `${change.label} (${change.old} → ${change.new})`
}

/**
 * Format the shared retry label from the reported fields:
 * - The operation and attempt.
 * - The wait.
 * - The error detail.
 *
 * All provider retry entries use this wording.
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
 * Flatten one structured entry into notification blocks.
 * flattenNotificationEntries combines group entries before this function receives them.
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
      // An aborted or failed compaction creates no completed context boundary.
      // Display its error as text rather than a divider.
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
 * Flatten a thread into notification blocks.
 * Group each consecutive run of group entries by groupKey.
 * Emit groups in the order that their keys first appear.
 * Keep the received order of entries within each group.
 * A non-group entry ends the run.
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
 * Read the latest resolvable post-compaction token count.
 * The context grid can then replace its stale pre-compaction count.
 *
 * Scan a consolidated wrapper from the last entry to the first.
 * Skip a boundary without a post count so an earlier complete boundary can supply it.
 *
 * The provider's compactionBoundaryFromMessage hook reads its native frame.
 * The shared reader does not parse provider shapes.
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
    // Pass each wrapper entry to the provider hook as a one-message parse.
    const post = boundary({ ...parsed, wrapper: null, topLevel: msg, parentObject: msg })?.post
    if (post !== undefined)
      return post
  }
  return undefined
}

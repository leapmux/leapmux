import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { CLAUDE_COMPACT_RESULT, CLAUDE_FRAME_KIND, CLAUDE_HIDDEN_SYSTEM_SUBTYPE, CLAUDE_HOOK_OUTCOME, CLAUDE_STATUS, CLAUDE_SYSTEM_SUBTYPE } from '~/generated/contracts/claude-protocol'
import { NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'
import { compactionMetaFromBoundary, retryEntry } from '../../../model/notification'
import { claudeRateLimitInfo } from '../rateLimits'

/**
 * The `system` subtypes LeapMux draws nothing for.
 *
 * This lookup reads the same contract table as the Worker's drop list.
 * contracts/claude-protocol.json states the reason for each entry.
 */
const HIDDEN_SYSTEM_SUBTYPES: Record<string, true> = Object.fromEntries(
  Object.values(CLAUDE_HIDDEN_SYSTEM_SUBTYPE).map(subtype => [subtype, true] as const),
)

/** Whether a `system` row of this subtype draws nothing at all. */
export function claudeSystemSubtypeHidden(subtype: string): boolean {
  return HIDDEN_SYSTEM_SUBTYPES[subtype] === true
}

/** Identify the native status that ends a failed compaction. */
function claudeCompactionFailed(m: Record<string, unknown>): boolean {
  return m.compact_result === CLAUDE_COMPACT_RESULT.Failed
}

const CLAUDE_STOP_HOOK_ERROR_KEY = 'stop-hook-error'

/** Claude's full compaction boundary. */
function isCompactBoundary(m: Record<string, unknown>): boolean {
  return m.type === 'system' && m.subtype === CLAUDE_SYSTEM_SUBTYPE.CompactBoundary
}

/**
 * Claude's MICROcompaction boundary.
 *
 * It carries no `compact_metadata`, so the row states the fact and no token
 * transition. Claude Code emits no metadata object for one.
 */
function isMicrocompactBoundary(m: Record<string, unknown>): boolean {
  return m.type === 'system' && m.subtype === CLAUDE_SYSTEM_SUBTYPE.MicrocompactBoundary
}

/**
 * The compaction boundary a Claude message states, or null when it states none.
 *
 * `agentEvents` reads this to refresh the context-usage grid the instant a boundary
 * lands -- outside the render tree, before any row model exists -- and
 * {@link claudeNotificationEntry} reads the same parse. One provider-owned reading, so
 * the grid and the transcript cannot disagree about what a boundary is.
 */
export function claudeCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const inner = getInnerMessage(parsed)
  if (!isObject(inner))
    return null
  if (isCompactBoundary(inner))
    return compactionMetaFromBoundary(inner)
  return isMicrocompactBoundary(inner) ? {} : null
}

/** Read the entries of one `system` frame of a known subtype. */
type ClaudeSystemReader = (m: Record<string, unknown>) => NotificationEntry[]

/** The line of a frame whose own `content` is the line that the interface shows. */
function contentLine(m: Record<string, unknown>): NotificationEntry[] {
  const text = pickString(m, 'content')
  return text ? [{ kind: 'text', text }] : []
}

/** The line of a plugin's `ui_log` or `ui_toast` frame, led by the plugin. */
function pluginLine(m: Record<string, unknown>): NotificationEntry[] {
  const text = pickString(m, 'text')
  if (!text)
    return []
  const plugin = pickString(m, 'plugin')
  return [{ kind: 'text', text: plugin ? `${plugin}: ${text}` : text }]
}

/** Claude's API-retry status. Each optional field appears only when the frame states it. */
function apiRetryEntries(m: Record<string, unknown>): NotificationEntry[] {
  const errorStatus = m.error_status != null ? String(m.error_status) : ''
  const attempt = pickNumber(m, 'attempt', undefined)
  const maxAttempts = pickNumber(m, 'max_retries', undefined)
  const delayMs = pickNumber(m, 'retry_delay_ms', undefined)
  const error = [errorStatus, pickString(m, 'error')].filter(Boolean).join(' ') || undefined
  return [retryEntry('api', {
    ...(attempt !== undefined ? { attempt } : {}),
    ...(maxAttempts !== undefined ? { maxAttempts } : {}),
    ...(delayMs !== undefined ? { delayMs } : {}),
    ...(error !== undefined ? { error } : {}),
  })]
}

/** A failed hook. A hook with any other outcome states nothing to show. */
function hookResponseEntries(m: Record<string, unknown>): NotificationEntry[] {
  if (pickString(m, 'outcome') !== CLAUDE_HOOK_OUTCOME.Error)
    return []
  const name = pickString(m, 'hook_name')
  const event = pickString(m, 'hook_event')
  const exitCode = pickNumber(m, 'exit_code', undefined)
  return [{
    kind: 'hook',
    outcome: 'failed',
    ...(event ? { event } : {}),
    ...(name ? { name } : {}),
    ...(exitCode === undefined ? {} : { exitCode }),
  }]
}

/**
 * The reader of each `system` subtype that draws a row, keyed by subtype.
 *
 * The table is also the list of the subtypes that this plugin knows. A reader
 * returns no entry for a frame that states no words, and the classifier then
 * hides the row (see {@link claudeReadsSystemSubtype}). A subtype that no entry
 * here lists keeps the no-display card, because that card is the signal that a
 * new frame exists.
 *
 * Only a failed compaction status supplies an entry. Compaction starts use live session state.
 */
const SYSTEM_SUBTYPE_READERS: Record<string, ClaudeSystemReader> = {
  [CLAUDE_SYSTEM_SUBTYPE.Status]: (m) => {
    if (!claudeCompactionFailed(m))
      return []
    const error = pickString(m, 'compact_error')
    return [{ kind: 'compaction', phase: 'end', outcome: 'failed', ...(error ? { error } : {}) }]
  },
  [CLAUDE_SYSTEM_SUBTYPE.ApiRetry]: apiRetryEntries,
  [CLAUDE_SYSTEM_SUBTYPE.CompactBoundary]: m => [{ kind: 'compaction', phase: 'end', detail: compactionMetaFromBoundary(m) }],
  [CLAUDE_SYSTEM_SUBTYPE.MicrocompactBoundary]: () => [{ kind: 'compaction', phase: 'end', micro: true }],
  [CLAUDE_SYSTEM_SUBTYPE.PermissionDenied]: (m) => {
    const tool = pickString(m, 'tool_name')
    const reason = pickString(m, 'decision_reason') || pickString(m, 'message')
    return [{ kind: 'text', text: [tool ? `Permission denied: ${tool}` : 'Permission denied', reason].filter(Boolean).join(' - ') }]
  },
  [CLAUDE_SYSTEM_SUBTYPE.Notification]: (m) => {
    if (m.key === CLAUDE_STOP_HOOK_ERROR_KEY)
      return [{ kind: 'hook', outcome: 'failed', event: 'Stop' }]
    // The "Error: " prefix alone states no error, so an empty text gets no line.
    const text = pickString(m, 'text')
    if (!text)
      return []
    return [{ kind: 'text', text: pickString(m, 'color') === 'error' ? `Error: ${text}` : text }]
  },
  [CLAUDE_SYSTEM_SUBTYPE.Informational]: contentLine,
  memory_recall: (m) => {
    // Claude Code 2.1.300 writes "Recalled N memories" for every scope. Its
    // line adds "local" only when a feature flag is on, and that build always
    // sets the flag off, so this line never adds it. A recall of no memory
    // states nothing that happened.
    const count = Array.isArray(m.memories) ? m.memories.filter(isObject).length : 0
    return count > 0 ? [{ kind: 'text', text: `Recalled ${count} ${count === 1 ? 'memory' : 'memories'}` }] : []
  },
  [CLAUDE_SYSTEM_SUBTYPE.HookResponse]: hookResponseEntries,
  ui_log: pluginLine,
  ui_toast: pluginLine,
  model_fallback: contentLine,
  [CLAUDE_SYSTEM_SUBTYPE.ModelRefusalFallback]: contentLine,
  [CLAUDE_SYSTEM_SUBTYPE.ModelConsentFallback]: contentLine,
}

/**
 * The reader of one `system` subtype, or undefined for a subtype that this
 * plugin does not know.
 *
 * `Object.hasOwn`, not a bare read: the subtype comes off the wire, and a value
 * that spells an `Object.prototype` member would answer with a function that
 * the caller would then run.
 */
function systemSubtypeReader(subtype: string): ClaudeSystemReader | undefined {
  return Object.hasOwn(SYSTEM_SUBTYPE_READERS, subtype) ? SYSTEM_SUBTYPE_READERS[subtype] : undefined
}

/**
 * Whether this plugin reads the words of a `system` subtype.
 *
 * The classifier asks this to choose the empty policy of a row. A known
 * subtype that states no words hides. An unknown subtype keeps the no-display
 * card.
 */
export function claudeReadsSystemSubtype(subtype: string): boolean {
  return systemSubtypeReader(subtype) !== undefined
}

/**
 * Read one Claude notification frame into the shared notification model.
 *
 * Every shape here is Claude's own:
 *
 * - the rate-limit event
 * - the failed compaction status
 * - each `system` subtype that {@link SYSTEM_SUBTYPE_READERS} lists
 *
 * They used to sit in the shared notification switch, where a second
 * provider's frame could reach them by accident. A frame that states no words
 * gets no entry.
 */
export function claudeNotificationEntry(m: Record<string, unknown>): NotificationEntry[] {
  if (m.type === NOTIFICATION_TYPE.RateLimitEvent) {
    const info = m.rate_limit_info
    if (!isObject(info)) {
      // A malformed payload still surfaces a generic line rather than vanishing --
      // `classify` routes it here only when the status is not "allowed", so it is a
      // real (if rare) notification.
      return [{ kind: 'text', text: 'Rate limit update' }]
    }
    return info.status === 'allowed' ? [] : [{ kind: 'rate-limit', tiers: [claudeRateLimitInfo(info)] }]
  }
  if (m.type !== CLAUDE_FRAME_KIND.System)
    return []
  return systemSubtypeReader(pickString(m, 'subtype'))?.(m) ?? []
}

/** Identify an old stored start without restoring its notification reader. */
export function claudeCompactionStart(message: Record<string, unknown>): boolean {
  return message.type === CLAUDE_FRAME_KIND.System
    && message.subtype === CLAUDE_SYSTEM_SUBTYPE.Status
    && message.status === CLAUDE_STATUS.Compacting
    && !claudeCompactionFailed(message)
}

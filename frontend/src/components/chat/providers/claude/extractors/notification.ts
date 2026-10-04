import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { CLAUDE_SYSTEM_SUBTYPE } from '~/generated/contracts/claude-protocol'
import { NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'
import { compactionMetaFromBoundary } from '../../../model/notification'
import { humanizeWireWord } from '../../../rendererUtils'
import { claudeRateLimitInfo } from '../rateLimits'

/**
 * The `system` subtypes LeapMux draws nothing for.
 *
 * Claude Code sends a `system` row for every step of its own bookkeeping, and
 * most of them state something the transcript already shows or something only a
 * machine reads. Naming each one HERE, with its reason, is what keeps the
 * classifier and the entry table from disagreeing about which rows exist:
 *
 *   - `init`, `session_state_changed` -- session lifecycle the worker consumes.
 *   - `task_notification`, `task_updated`, `background_tasks_changed` -- the
 *     background-task registry, which the sidebar draws from its own store.
 *   - `thinking_tokens`, `api_metrics`, `turn_duration` -- telemetry. The
 *     turn-end divider already states the duration.
 *   - `hook_started`, `hook_progress`, `hook_response` -- a hook that BLOCKS a
 *     call reports itself in that call's own result, and the rest is noise at
 *     one row per hook per turn.
 *   - `files_persisted`, `memory_saved` -- a write the agent made to its own
 *     state, not to the workspace.
 *   - `elicitation_complete` -- the elicitation row already states the answer.
 *   - `local_command_output`, `local_command`, `informational`,
 *     `permission_retry`, `stop_hook_summary`, `away_summary` -- rows the
 *     command line interface draws in ITS transcript, whose content LeapMux
 *     shows through its own surfaces.
 */
const HIDDEN_SYSTEM_SUBTYPES: ReadonlySet<string> = new Set([
  'init',
  'session_state_changed',
  'task_notification',
  'task_updated',
  'background_tasks_changed',
  'thinking_tokens',
  'api_metrics',
  'turn_duration',
  'hook_started',
  'hook_progress',
  'hook_response',
  'files_persisted',
  'memory_saved',
  'elicitation_complete',
  'local_command_output',
  'local_command',
  'informational',
  'permission_retry',
  'stop_hook_summary',
  'away_summary',
])

/** Whether a `system` row of this subtype draws nothing at all. */
export function claudeSystemSubtypeHidden(subtype: string): boolean {
  return HIDDEN_SYSTEM_SUBTYPES.has(subtype)
}

/**
 * The summary Claude Code writes at the end of a turn.
 *
 * `status_category` says what state the turn left the work in -- blocked,
 * waiting, review_ready and failed all mean the reader has something to do -- so
 * it leads the line for every category but the one that says the work finished.
 */
function postTurnSummaryText(m: Record<string, unknown>): string {
  const title = pickString(m, 'title') || pickString(m, 'description')
  if (!title)
    return ''
  const category = pickString(m, 'status_category')
  return category && category !== 'completed' ? `${humanizeWireWord(category)}: ${title}` : title
}

/**
 * Claude's in-progress compaction: `{type:system,subtype:status,status:compacting}`.
 *
 * The TRAILING status (`status:null`) closes the compaction and carries nothing new,
 * so `isFinalCompactingStatus` hides it; only this one draws.
 */
function isCompactingStatus(m: Record<string, unknown>): boolean {
  return m.type === 'system' && m.subtype === 'status' && m.status === NOTIFICATION_TYPE.Compacting
}

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

/**
 * Read one Claude notification frame into the shared notification model.
 *
 * Every shape here is Claude's own: the rate-limit event, the API-retry status, and
 * the three compaction boundaries. They used to sit in the shared notification switch,
 * where a second provider's frame could reach them by accident.
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
  if (m.type === 'system' && m.subtype === CLAUDE_SYSTEM_SUBTYPE.ApiRetry) {
    const errorStatus = m.error_status != null ? String(m.error_status) : ''
    const attempt = pickNumber(m, 'attempt', undefined)
    const maxAttempts = pickNumber(m, 'max_retries', undefined)
    const delayMs = pickNumber(m, 'retry_delay_ms', undefined)
    const error = [errorStatus, pickString(m, 'error')].filter(Boolean).join(' ') || undefined
    return [{
      kind: 'retry',
      scope: 'api',
      // Each optional half rides only when the frame stated it.
      ...(attempt !== undefined ? { attempt } : {}),
      ...(maxAttempts !== undefined ? { maxAttempts } : {}),
      ...(delayMs !== undefined ? { delayMs } : {}),
      ...(error !== undefined ? { error } : {}),
    }]
  }
  // `/clear`, and the plan exit that starts a fresh conversation. The worker
  // rewrites it into the neutral `context_cleared` type, so this branch serves a
  // transcript recorded before it did.
  if (m.type === 'conversation_reset')
    return [{ kind: 'context-cleared' }]
  if (m.type === 'system' && m.subtype === 'post_turn_summary') {
    const text = postTurnSummaryText(m)
    return text ? [{ kind: 'text', text }] : []
  }
  if (m.type === 'system' && m.subtype === 'permission_denied') {
    const tool = pickString(m, 'tool_name')
    const reason = pickString(m, 'decision_reason') || pickString(m, 'message')
    return [{ kind: 'text', text: [tool ? `Permission denied: ${tool}` : 'Permission denied', reason].filter(Boolean).join(' - ') }]
  }
  if (m.type === 'system' && m.subtype === 'api_error') {
    const detail = pickString(m, 'error') || pickString(m, 'message')
    return [{ kind: 'text', text: detail ? `API error: ${detail}` : 'API error' }]
  }
  if (isCompactingStatus(m))
    return [{ kind: 'compaction', phase: 'start' }]
  if (isCompactBoundary(m))
    return [{ kind: 'compaction', phase: 'end', detail: compactionMetaFromBoundary(m) }]
  if (isMicrocompactBoundary(m))
    return [{ kind: 'compaction', phase: 'end', micro: true }]
  return []
}

import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { KIMI_EVENT, KIMI_ORIGIN } from '~/generated/contracts/kimi-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { KIMI_GOAL_CONTINUATION, kimiEvent, kimiEventData } from '../protocol'

/**
 * Why the agent started a turn by itself, per `turn.started.origin.kind`.
 *
 * The worker records such a turn because nothing else in the transcript says why the
 * agent speaks. A user turn, and a turn that follows a task notification, never reach a
 * row: the user's message and the notification already state the cause.
 *
 * A Map, for the reason `kimi/toolKinds.ts` gives: the key is a WIRE word.
 */
const KIMI_ORIGIN_PHRASES: ReadonlyMap<string, (name: string) => string> = new Map([
  [KIMI_ORIGIN.SystemTrigger, (name: string) => name === KIMI_GOAL_CONTINUATION ? 'Continuing the goal' : name ? `Started by ${name}` : 'Started by the agent'],
  [KIMI_ORIGIN.CronJob, (name: string) => name ? `Scheduled prompt ${name} fired` : 'A scheduled prompt fired'],
  [KIMI_ORIGIN.CronMissed, (name: string) => name ? `Missed scheduled prompt ${name} ran late` : 'A missed scheduled prompt ran late'],
  [KIMI_ORIGIN.Retry, () => 'Retrying the turn'],
  [KIMI_ORIGIN.HookResult, (name: string) => name ? `Hook ${name} started a turn` : 'A hook started a turn'],
  [KIMI_ORIGIN.SkillActivation, (name: string) => name ? `Skill ${name} started a turn` : 'A skill started a turn'],
  [KIMI_ORIGIN.PluginCommand, (name: string) => name ? `Plugin command ${name} started a turn` : 'A plugin command started a turn'],
  [KIMI_ORIGIN.Injection, () => 'The agent received injected context'],
  [KIMI_ORIGIN.ShellCommand, (name: string) => name ? `Shell command ${name} started a turn` : 'A shell command started a turn'],
  [KIMI_ORIGIN.CompactionSummary, () => 'Continuing from the compacted context'],
])

/**
 * Read one Kimi Code notification row into the shared notification model.
 *
 * The SOLE notification seam, for a standalone row and for one entry of a consolidated
 * thread alike. Returns an empty list for a row this provider does not own, and for a
 * row of its own that states nothing it can word.
 */
export function kimiNotificationEntry(msg: Record<string, unknown>): NotificationEntry[] {
  const event = kimiEvent(msg)
  if (!event)
    return []
  const data = event.data
  switch (event.type) {
    case KIMI_EVENT.TurnStarted: {
      const origin = pickObject(data, 'origin')
      const phrase = KIMI_ORIGIN_PHRASES.get(pickString(origin, 'kind'))
      return phrase ? [{ kind: 'text', text: phrase(pickString(origin, 'name')) }] : []
    }
    case KIMI_EVENT.TurnStepRetrying: {
      const attempt = pickNumber(data, 'nextAttempt')
      const maxAttempts = pickNumber(data, 'maxAttempts')
      const delayMs = pickNumber(data, 'delayMs')
      const error = pickString(data, 'errorMessage') || pickString(data, 'errorName')
      return [{
        kind: 'retry',
        scope: 'api',
        ...(attempt !== null ? { attempt } : {}),
        ...(maxAttempts !== null ? { maxAttempts } : {}),
        ...(delayMs !== null ? { delayMs } : {}),
        ...(error ? { error } : {}),
      }]
    }
    case KIMI_EVENT.CompactionStarted:
      return [{ kind: 'compaction', phase: 'start', ...(pickString(data, 'trigger') ? { detail: { trigger: pickString(data, 'trigger') } } : {}) }]
    case KIMI_EVENT.CompactionCompleted:
      return [{ kind: 'compaction', phase: 'end', detail: kimiCompactionDetails(data) }]
    case KIMI_EVENT.CompactionBlocked:
      return [{ kind: 'status', text: 'Compaction is waiting for the running turn to end' }]
    case KIMI_EVENT.CompactionCancelled:
      return [{ kind: 'status', text: 'Compaction cancelled' }]
    case KIMI_EVENT.Warning: {
      const message = pickString(data, 'message')
      return message ? [{ kind: 'text', text: `Warning: ${message}` }] : []
    }
    case KIMI_EVENT.Error: {
      const message = pickString(data, 'message')
      const code = pickString(data, 'code')
      if (!message && !code)
        return []
      return [{ kind: 'text', text: code && message ? `Error (${code}): ${message}` : `Error: ${message || code}` }]
    }
    case KIMI_EVENT.TaskNotified: {
      const title = pickString(data, 'title')
      const body = pickString(data, 'body')
      const text = [title, body].filter(Boolean).join(': ')
      return text ? [{ kind: 'text', text }] : []
    }
    default:
      return []
  }
}

/** What a compaction states about the context it rewrote. */
function kimiCompactionDetails(data: Record<string, unknown>): CompactionDetails {
  const result = pickObject(data, 'result')
  const pre = pickNumber(result, 'tokensBefore')
  const post = pickNumber(result, 'tokensAfter')
  return { ...(pre !== null ? { pre } : {}), ...(post !== null ? { post } : {}) }
}

/**
 * The compaction boundary one row states, or null for any other row.
 *
 * The context-usage grid reads it the moment the boundary lands, so the gauge drops to
 * the compacted size before the next usage report arrives.
 */
export function kimiCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const data = kimiEventData(parsed.parentObject, KIMI_EVENT.CompactionCompleted)
  return data ? kimiCompactionDetails(data) : null
}

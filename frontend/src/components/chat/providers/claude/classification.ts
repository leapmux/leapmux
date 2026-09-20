import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationContext, ClassificationInput } from '../registry'
import { NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isFinalCompactingStatus, isNotificationThreadWrapper } from '../../messageUtils'
import { claudeSystemSubtypeHidden } from './extractors/notification'
import { claudeExitPlanText } from './extractors/plan'
import { canonicalClaudeToolName, claudeToolRowHidden } from './toolKinds'

/**
 * The extra notification type for Claude Code.
 *
 * Claude Code emits `rate_limit_event`, and Claude alone reads it. It stays out of the
 * shared base set, where it makes every other provider's wrapper test accept a type
 * that provider never sends.
 *
 * `plan_execution` needs no entry beside it. `BASE_NOTIFICATION_TYPES` holds that type,
 * because the worker writes it for every provider.
 */
const CLAUDE_EXTRA_TYPES = new Set<string>([NOTIFICATION_TYPE.RateLimitEvent])

function isClaudeNotifThread(wrapper: { messages: unknown[] } | null): wrapper is { messages: unknown[] } {
  return isNotificationThreadWrapper(wrapper, CLAUDE_EXTRA_TYPES, (t, st) =>
    t === 'system' && !claudeSystemSubtypeHidden(st ?? ''))
}

/**
 * Per-message hidden rules shared by the standalone `system`/`rate_limit_event`
 * classifiers and the consolidated-thread filter, so a notification that is
 * hidden on its own stays hidden when Hub threads it into a
 * `notification_thread` wrapper. Without this single source of truth the two
 * paths drift: the wrapper branch used to drop only allowed `rate_limit_event`s,
 * so a final compaction status leaked through as a `notification` and
 * rendered as raw JSON.
 *
 * Covers the type/subtype-driven rules only. The `task_started`/`task_progress`
 * rule needs the envelope's `parentSpanId` (absent from a consolidated inner
 * message), so it stays inline in the `system` classifier.
 *
 * - `rate_limit_event` whose `rate_limit_info.status` is "allowed" -- a no-op
 *   refresh, not a throttle the user needs to see.
 * - `system` whose subtype {@link claudeSystemSubtypeHidden} lists.
 * - `system` `status` updates other than the live "compacting" one -- e.g. the
 *   final `{status:null, compact_result:"success"}` ending a compaction. The
 *   user-facing "Context compacted (...)" line comes from compact_boundary, so
 *   this final status carries nothing to show.
 */
function isHiddenClaudeNotification(m: Record<string, unknown>): boolean {
  const type = pickString(m, 'type')
  if (type === 'rate_limit_event') {
    const info = pickObject(m, 'rate_limit_info')
    return info?.status === 'allowed'
  }
  if (type === 'system') {
    if (claudeSystemSubtypeHidden(pickString(m, 'subtype')))
      return true
    if (isFinalCompactingStatus(m))
      return true
  }
  return false
}

type ClaudeTypeClassifier = (
  parent: Record<string, unknown>,
  input: ClassificationInput,
  context?: ClassificationContext,
) => MessageCategory

/**
 * Classifiers for type-keyed Claude messages whose result preempts
 * `isCompactSummary` and synthetic-control-response checks. These are
 * notification-shaped: their type alone determines the category.
 *
 * Every entry here needs a rule of its OWN. A type that only draws as a plain row
 * belongs to {@link isPlainNotificationType}, which `classifyClaudeCodeMessage` asks
 * right after this table misses -- this table once restated four of those seven types
 * and drew the raw frame for the other three.
 */
const CLAUDE_NOTIFICATION_CLASSIFIERS: Record<string, ClaudeTypeClassifier> = {
  system(parent, input) {
    const subtype = pickString(parent, 'subtype')
    if (input.parentSpanId && (subtype === 'task_started' || subtype === 'task_progress'))
      return { kind: 'hidden' }
    if (isHiddenClaudeNotification(parent))
      return { kind: 'hidden' }
    return { kind: 'notification', messages: [parent] }
  },
  rate_limit_event(parent) {
    if (isHiddenClaudeNotification(parent))
      return { kind: 'hidden' }
    return { kind: 'notification', messages: [parent] }
  },
  // `/clear`, and the plan exit that starts a new conversation. A transcript
  // recorded before the worker rewrote it still carries this type.
  conversation_reset: parent => ({ kind: 'notification', messages: [parent] }),
  result: () => ({ kind: 'result_divider' }),
}

/**
 * Classifiers for content-shaped Claude messages (`assistant`/`user`). These
 * run AFTER the `isCompactSummary` / synthetic-control-response guards so
 * that those flags can preempt the content dispatch.
 */
const CLAUDE_CONTENT_CLASSIFIERS: Record<string, ClaudeTypeClassifier> = {
  assistant(parent, input) {
    const message = pickObject(parent, 'message')
    if (!message)
      return { kind: 'unknown' }
    const content = message.content
    if (!Array.isArray(content))
      return { kind: 'unknown' }
    // `unknown[]`, not the array of records a cast used to promise: the blocks arrive
    // off the wire, and every read below narrows each one before it touches a field.
    const blocks: unknown[] = content
    const toolUse = blocks.find((c): c is Record<string, unknown> => isObject(c) && c.type === 'tool_use')
    if (toolUse) {
      const toolName = pickString(toolUse, 'name')
      if (claudeToolRowHidden(canonicalClaudeToolName(toolName || input.spanType || ''), 'request'))
        return { kind: 'hidden' }
      // A proposed plan leaves the tool path: every provider draws one the same
      // way, through the shared plan card. BOTH layers read it here, so the row
      // the list measures and the row the transcript draws cannot disagree.
      return claudeExitPlanText(toolUse) ? { kind: 'assistant_plan' } : { kind: 'tool_use' }
    }
    if (blocks.some(c => isObject(c) && c.type === 'text'))
      return { kind: 'assistant_text' }
    if (blocks.some(c => isObject(c) && c.type === 'thinking')) {
      // Signature-only thinking blocks (no visible text) can slip past
      // --thinking-display summarized; hide them so the UI doesn't render
      // an empty row.
      const hasText = blocks.some(c =>
        isObject(c) && c.type === 'thinking'
        && typeof c.thinking === 'string' && c.thinking.length > 0)
      return hasText ? { kind: 'assistant_thinking' } : { kind: 'hidden' }
    }
    return { kind: 'unknown' }
  },
  user(parent, input, context) {
    // The span column states the tool on every row of a span, so one test answers
    // for a result row whose own bytes state no tool -- `EnterPlanMode` carries
    // no `tool_result` block at all, and its row is hidden all the same.
    // `||`, not `??`: `spanType` is a protobuf string, so an unset column is `''`
    // and never undefined -- `??` would make the payload fallback unreachable.
    const spanTool = canonicalClaudeToolName(String(input.spanType || parent.span_type || ''))
    if (spanTool && claudeToolRowHidden(spanTool, 'result'))
      return { kind: 'hidden' }

    const message = pickObject(parent, 'message')
    if (message) {
      const content = message.content
      if (typeof content === 'string')
        return { kind: 'user_text' }
      if (Array.isArray(content)) {
        // tool_result takes priority over agent_prompt (subagent tool results
        // also have parent_tool_use_id but should be rendered as tool results).
        if (content.some(c => isObject(c) && c.type === 'tool_result'))
          return { kind: 'tool_result' }
      }
    }
    // A user message carrying parent_tool_use_id is the prompt sent TO a
    // subagent -- but only in the transcript that spawned it. Inside the
    // subagent's OWN transcript every forwarded message carries that same id,
    // including its interrupt notices and local command output, and none of
    // those is a prompt. The child's real prompt is persisted separately, as a
    // plain user message, so nothing here is ever one.
    if (typeof parent.parent_tool_use_id === 'string')
      return context?.isChildTranscript ? { kind: 'user_text' } : { kind: 'agent_prompt' }
    return { kind: 'unknown' }
  },
}

/** Claude Code message classification. */
export function classifyClaudeCodeMessage(
  input: ClassificationInput,
  context?: ClassificationContext,
): MessageCategory {
  const parentObject = input.parentObject
  const wrapper = input.wrapper

  // Empty wrapper (all notifications consolidated to no-ops) — hide.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }

  // Notification thread (wrapper with notification-type first message). Drop the
  // per-message hidden shapes (the same ones the standalone classifiers hide) so
  // a thread of only-hidden entries collapses to `hidden` rather than surfacing
  // an empty notification or a raw-JSON fallback.
  if (isClaudeNotifThread(wrapper)) {
    const msgs = wrapper.messages.filter(m => !isObject(m) || !isHiddenClaudeNotification(m))
    if (msgs.length === 0)
      return { kind: 'hidden' }
    return { kind: 'notification', messages: msgs }
  }

  if (!parentObject)
    return { kind: 'unknown' }

  const type = pickString(parentObject, 'type')

  // Notification-shaped types preempt compact-summary / control-response.
  if (type) {
    // `Object.hasOwn`, not a bare read: `type` comes straight off the wire, and a
    // value that spells an `Object.prototype` member answers with a function the
    // call below would then run.
    const notif = Object.hasOwn(CLAUDE_NOTIFICATION_CLASSIFIERS, type) ? CLAUDE_NOTIFICATION_CLASSIFIERS[type] : undefined
    if (notif)
      return notif(parentObject, input)
  }

  // The LeapMux envelope every provider answers the same way. The shared predicate is
  // the one list, and it sits at the same depth the table above sits at, so a plain row
  // still preempts the compact-summary read below.
  if (isPlainNotificationType(type))
    return { kind: 'notification', messages: [parentObject] }

  // Compact summary preempts content-shaped types.
  if (parentObject.isCompactSummary === true)
    return { kind: 'compact_summary' }

  // (The synthetic {isSynthetic, controlResponse} row -> control_response is classified upstream in
  // classifyMessage, before any plugin?.transcript.classify runs, since it is a LeapMux-neutral shape.)

  // Content-shaped types (assistant / user).
  if (type) {
    // `Object.hasOwn`, for the reason the notification lookup above gives.
    const content = Object.hasOwn(CLAUDE_CONTENT_CLASSIFIERS, type) ? CLAUDE_CONTENT_CLASSIFIERS[type] : undefined
    if (content)
      return content(parentObject, input, context)
  }

  // Plain object with string .content and no .type → user_content (or hidden /
  // plan_execution variants).
  if (!type && typeof parentObject.content === 'string') {
    if (parentObject.hidden === true)
      return { kind: 'hidden' }
    if (parentObject.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }

  return { kind: 'unknown' }
}

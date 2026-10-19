import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationContext, ClassificationInput } from '../registry'
import { CLAUDE_FRAME_KIND, CLAUDE_SYSTEM_SUBTYPE } from '~/generated/contracts/claude-protocol'
import { NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { classifyNotifications } from '../../notificationClassification'
import { claudeNotificationEntry, claudeReadsSystemSubtype, claudeSystemSubtypeHidden } from './extractors/notification'
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
    t === CLAUDE_FRAME_KIND.System && !claudeSystemSubtypeHidden(st ?? ''))
}

/**
 * Apply the same hidden rules to standalone notices and members of a notification thread.
 * An allowed rate-limit refresh needs no row. The system-subtype rule reads the Worker's shared contract table.
 */
function isHiddenClaudeNotification(m: Record<string, unknown>): boolean {
  const type = pickString(m, 'type')
  if (type === 'rate_limit_event') {
    const info = pickObject(m, 'rate_limit_info')
    return info?.status === 'allowed'
  }
  if (type === CLAUDE_FRAME_KIND.System) {
    const subtype = pickString(m, 'subtype')
    if (claudeSystemSubtypeHidden(subtype))
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
 * synthetic-control-response checks. These are
 * notification-shaped: their type alone determines the category.
 *
 * Every entry here needs a rule of its OWN. A type that only draws as a plain row
 * belongs to {@link isPlainNotificationType}, which `classifyClaudeCodeMessage` asks
 * right after this table misses -- this table once restated four of those seven types
 * and drew the raw frame for the other three.
 */
const CLAUDE_NOTIFICATION_CLASSIFIERS: Record<string, ClaudeTypeClassifier> = {
  [CLAUDE_FRAME_KIND.System](parent, input) {
    const subtype = pickString(parent, 'subtype')
    if (isHiddenClaudeNotification(parent))
      return { kind: 'hidden' }
    if (subtype === CLAUDE_SYSTEM_SUBTYPE.Status && claudeNotificationEntry(parent).length === 0)
      return { kind: 'unknown' }
    // A known subtype that states no words draws nothing. An unknown subtype
    // keeps the no-display card, because that card is the signal that a new
    // frame exists.
    return classifyNotifications([parent], input.agentProvider, claudeNotificationEntry, claudeReadsSystemSubtype(subtype) ? 'hidden' : 'notification')
  },
  rate_limit_event(parent, input) {
    if (isHiddenClaudeNotification(parent))
      return { kind: 'hidden' }
    return classifyNotifications([parent], input.agentProvider, claudeNotificationEntry)
  },
  [CLAUDE_FRAME_KIND.Result]: () => ({ kind: 'result_divider' }),
}

/**
 * Classifiers for content-shaped Claude messages (`assistant`/`user`). These
 * run after the neutral control-response check.
 */
const CLAUDE_CONTENT_CLASSIFIERS: Record<string, ClaudeTypeClassifier> = {
  [CLAUDE_FRAME_KIND.Assistant](parent, input) {
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
  [CLAUDE_FRAME_KIND.User](parent, input) {
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
      if (Array.isArray(content)) {
        // Forwarded tool results keep the same category as root tool results.
        if (content.some(c => isObject(c) && c.type === 'tool_result'))
          return { kind: 'tool_result' }
      }
    }
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

  // Hide an empty wrapper after the reducer removes every entry.
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
    return classifyNotifications(msgs, input.agentProvider, claudeNotificationEntry)
  }

  if (!parentObject)
    return { kind: 'unknown' }

  const type = pickString(parentObject, 'type')

  // Read native notification types before content-shaped types.
  if (type) {
    // `Object.hasOwn`, not a bare read: `type` comes straight off the wire, and a
    // value that spells an `Object.prototype` member answers with a function the
    // call below would then run.
    const notif = Object.hasOwn(CLAUDE_NOTIFICATION_CLASSIFIERS, type) ? CLAUDE_NOTIFICATION_CLASSIFIERS[type] : undefined
    if (notif)
      return notif(parentObject, input)
  }

  // The shared predicate identifies worker envelopes before native content parsing.
  if (isPlainNotificationType(type))
    return classifyNotifications([parentObject], input.agentProvider, claudeNotificationEntry)

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

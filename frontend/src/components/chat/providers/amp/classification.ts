import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { AMP_BLOCK_TYPE, AMP_LINE_TYPE } from '~/generated/contracts/amp-protocol'
import { pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { retainedRowIsFinal } from '../registry'
import { ampBlockText, ampMessageBlocks, ampToolResult, ampToolUse } from './extractors/toolCommon'

/** The block types an assistant row can carry that this build reads. */
const KNOWN_ASSISTANT_BLOCKS = new Set<string>([
  AMP_BLOCK_TYPE.Text,
  AMP_BLOCK_TYPE.Thinking,
  AMP_BLOCK_TYPE.RedactedThinking,
  AMP_BLOCK_TYPE.ToolUse,
])

/**
 * The category of one assistant row.
 *
 * A tool call wins, because it owns a span. A turn that ended while the call ran
 * stores the call's row AGAIN as the closing row, and the completion is what tells
 * that copy from the request. Text wins over thinking, which a row the worker kept
 * whole can hold beside it. An empty block states nothing: an OpenAI model sends an
 * empty thinking block for its encrypted reasoning, and a redacted block holds only
 * data that no reader can read.
 */
function classifyAssistantRow(input: ClassificationInput, parent: Record<string, unknown>): MessageCategory {
  if (ampToolUse(parent))
    return retainedRowIsFinal(input.completion) ? { kind: 'tool_result' } : { kind: 'tool_use' }
  if (ampBlockText(parent, AMP_BLOCK_TYPE.Text, 'text'))
    return { kind: 'assistant_text' }
  if (ampBlockText(parent, AMP_BLOCK_TYPE.Thinking, 'thinking'))
    return { kind: 'assistant_thinking' }
  // A block this build does not know reaches the reader as the raw frame, rather than
  // disappearing.
  const unknownBlock = ampMessageBlocks(parent).some(block => !KNOWN_ASSISTANT_BLOCKS.has(pickString(block, 'type')))
  return unknownBlock ? { kind: 'unknown' } : { kind: 'hidden' }
}

/** Amp message classification. */
export function classifyAmpMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider)

  // The empty-wrapper test runs FIRST, so the thread test below stays the one
  // narrowing on `wrapper`. Amp writes no notification of its own, so a thread holds
  // LeapMux's own envelopes alone.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }
  if (isNotificationThreadWrapper(wrapper))
    return notification(wrapper.messages, 'hidden')

  if (!parent)
    return { kind: 'unknown' }

  // A user row the service layer persisted is LeapMux's `{content}` shape, with no Amp
  // `type`. Amp's own echo of the user's message never reaches the transcript.
  const type = pickString(parent, 'type')
  if (!type && typeof parent.content === 'string') {
    if (parent.hidden === true)
      return { kind: 'hidden' }
    if (parent.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }

  switch (type) {
    case AMP_LINE_TYPE.Result:
      return { kind: 'result_divider' }
    case AMP_LINE_TYPE.Assistant:
      return classifyAssistantRow(input, parent)
    case AMP_LINE_TYPE.User:
      // A user row reaches the transcript only as a tool result.
      return ampToolResult(parent) ? { kind: 'tool_result' } : { kind: 'hidden' }
    default:
      break
  }

  // LeapMux's own envelope. No branch above claims one of these rows, because Amp's
  // vocabulary spells none of their tokens.
  if (isPlainNotificationType(type))
    return notification([parent])

  // A `system` line other than the init line, or a line of a later Amp: the worker
  // keeps it so that a reader can inspect it.
  return { kind: 'unknown' }
}

import type { ResolvedMessageContent } from '~/components/chat/rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { isObject, pickString } from '~/lib/jsonPick'
import { retainedRowIsFinal } from '../registry'

/**
 * Which half of a call one Qoder frame carries: an assistant frame with a `tool_use`
 * block, or a user frame with a `tool_result` block. Undefined for any other frame.
 */
function qoderToolFrame(parsed: ResolvedMessageContent): 'tool_use' | 'tool_result' | undefined {
  const parent = parsed.parentObject
  if (!parent || !isObject(parent))
    return undefined
  const type = pickString(parent, 'type')
  const message = isObject(parent.message)
    ? parent.message
    : undefined
  const content = message && Array.isArray(message.content)
    ? message.content
    : []
  const blockType = (name: string) =>
    content.some(block => isObject(block) && pickString(block, 'type') === name)

  if (type === 'assistant' && blockType('tool_use'))
    return 'tool_use'
  if (type === 'user' && blockType('tool_result'))
    return 'tool_result'
  return undefined
}

/**
 * The role of one Qoder row inside its tool span.
 *
 * An assistant frame carries the `tool_use` block, so it is the request. A user
 * frame carries the `tool_result` block, so it is the result.
 */
export function qoderSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  switch (qoderToolFrame(parsed)) {
    // A turn that ends while Qoder streams a call stores the `tool_use` frame with a
    // completion, and no result follows it: that frame is the final row of its span.
    case 'tool_use':
      return retainedRowIsFinal(parsed.completion)
        ? 'result'
        : 'request'
    case 'tool_result':
      return 'result'
    case undefined:
      return 'other'
  }
}

/**
 * A `tool_result` block states neither the tool name nor the arguments, so a result
 * needs its exact request. A `tool_use` frame draws from its own block only, also when a
 * turn's end made it the final row of its span.
 */
export function qoderNeedsRequest(parsed: ResolvedMessageContent): boolean {
  return qoderToolFrame(parsed) === 'tool_result'
}

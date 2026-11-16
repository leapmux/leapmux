import type { ResolvedMessageContent } from '~/components/chat/rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { isObject, pickString } from '~/lib/jsonPick'
import { retainedRowIsFinal } from '../registry'
import { storedFunctionCallID, storedFunctionIsProgress } from './storedFunction'

/**
 * The role of one CodeBuddy row inside its tool span.
 *
 * An assistant frame carries the `tool_use` block, so it is the request. A user
 * frame carries the `tool_result` block, so it is the result.
 */
export function codebuddySpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  switch (codebuddyToolFrame(parsed)) {
    // A turn that ended while the call ran stores the request frame AGAIN, with a
    // completion, as the closing row: the completion column separates the two copies.
    case 'request':
      return retainedRowIsFinal(parsed.completion)
        ? 'result'
        : 'request'
    case 'result':
      return 'result'
    case undefined:
      return 'other'
  }
}

/**
 * Which half of a call one CodeBuddy frame carries, by its shape alone: a stored
 * `function_call` record or an assistant frame with a `tool_use` block opens the call,
 * and a final stored output record or a user frame with a `tool_result` block ends it.
 * Undefined for any other frame, a progress record included.
 */
function codebuddyToolFrame(parsed: ResolvedMessageContent): 'request' | 'result' | undefined {
  const parent = parsed.parentObject
  if (!parent || !isObject(parent))
    return undefined
  const type = pickString(parent, 'type')
  if (type === 'function_call') {
    return storedFunctionCallID(parent)
      ? 'request'
      : undefined
  }
  if (type === 'function_call_output' || type === 'function_call_result') {
    return storedFunctionCallID(parent) && !storedFunctionIsProgress(parent)
      ? 'result'
      : undefined
  }
  const message = isObject(parent.message)
    ? parent.message
    : undefined
  const content = message && Array.isArray(message.content)
    ? message.content
    : []
  const blockType = (name: string) =>
    content.some(block => isObject(block) && pickString(block, 'type') === name
      && pickString(block, name === 'tool_use'
        ? 'id'
        : 'tool_use_id').trim() !== '')

  if (type === 'assistant' && blockType('tool_use'))
    return 'request'
  if (type === 'user' && blockType('tool_result'))
    return 'result'
  return undefined
}

/**
 * Whether one row draws from its span's request: a result frame, live or stored, states
 * neither the tool's name nor its arguments, so it reads them from the request, whatever
 * completion LeapMux stored with it. A request frame, and the closing copy of it that a
 * turn's end leaves, reads its own frame.
 */
export function codebuddyNeedsRequest(parsed: ResolvedMessageContent): boolean {
  return codebuddyToolFrame(parsed) === 'result'
}

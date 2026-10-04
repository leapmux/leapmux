import type { ResolvedMessageContent } from '~/components/chat/rowExtractionTypes'
import type { ToolSpanRole, ToolSpanSide } from '~/lib/messageSpan'
import { isObject, pickString } from '~/lib/jsonPick'
import { storedFunctionCallID, storedFunctionIsProgress } from './storedFunction'

/**
 * The role of one CodeBuddy row inside its tool span.
 *
 * An assistant frame carries the `tool_use` block, so it is the request. A user
 * frame carries the `tool_result` block, so it is the result.
 */
export function codebuddySpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const parent = parsed.parentObject
  if (!parent || !isObject(parent))
    return 'other'
  const type = pickString(parent, 'type')
  if (type === 'function_call')
    return storedFunctionCallID(parent) ? 'request' : 'other'
  if (type === 'function_call_output' || type === 'function_call_result')
    return storedFunctionCallID(parent) && !storedFunctionIsProgress(parent) ? 'result' : 'other'
  const message = isObject(parent.message) ? parent.message : undefined
  const content = message && Array.isArray(message.content) ? message.content : []
  const blockType = (name: string) =>
    content.some(block => isObject(block) && pickString(block, 'type') === name
      && pickString(block, name === 'tool_use' ? 'id' : 'tool_use_id').trim() !== '')

  if (type === 'assistant' && blockType('tool_use'))
    return 'request'
  if (type === 'user' && blockType('tool_result'))
    return 'result'
  return 'other'
}

/** Native result records need the request's tool name and arguments. */
export function codebuddyRelatedMessages(parsed: ResolvedMessageContent): readonly ToolSpanSide[] {
  return codebuddySpanRole(parsed) === 'result' ? ['request'] : []
}

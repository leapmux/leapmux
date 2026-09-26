import type { ResolvedMessageContent } from '~/components/chat/rowExtractionTypes'
import type { ToolSpanRole, ToolSpanSide } from '~/lib/messageSpan'
import { isObject, pickString } from '~/lib/jsonPick'

/**
 * The role of one Qoder row inside its tool span.
 *
 * An assistant frame carries the `tool_use` block, so it is the request. A user
 * frame carries the `tool_result` block, so it is the result.
 */
export function qoderSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const parent = parsed.parentObject
  if (!parent || !isObject(parent))
    return 'other'
  const type = pickString(parent, 'type')
  const message = isObject(parent.message) ? parent.message : undefined
  const content = message && Array.isArray(message.content) ? message.content : []
  const blockType = (name: string) =>
    content.some(block => isObject(block) && pickString(block, 'type') === name)

  if (type === 'assistant' && blockType('tool_use'))
    return 'request'
  if (type === 'user' && blockType('tool_result'))
    return 'result'
  return 'other'
}

/** Qoder rows are self-contained; no side lookups needed. */
export function qoderRelatedMessages(_parsed: ResolvedMessageContent): readonly ToolSpanSide[] {
  return []
}

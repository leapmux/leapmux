import type { ParsedMessageContent } from '~/lib/messageParser'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { getMessageContent } from '~/lib/contentBlocks'
import { isObject, pickString } from '~/lib/jsonPick'
import { PI_EVENT } from './providers/pi/protocol'

/**
 * The role a spanId message plays in a tool span: the tool_use `opener`, the
 * tool_result, or neither. The chat store's span index routes by this to pair a
 * tool_use bubble with its result and vice versa.
 *
 * Deliberately a LEAF module -- it imports only constants and leaf libs, never
 * the provider registry (`./providers`) or the chat store -- so `chatSpanIndex`
 * can classify by the real per-provider rules without forming the value-import
 * cycle that pulling in the full `classifyMessage` path would (the registry's
 * extractors value-import back from `~/stores/chat.store`).
 *
 * Provider dialects mark opener vs result differently:
 *  - Claude (Anthropic shape): a `tool_use` / `tool_result` content block.
 *  - Pi: a flat envelope whose `type` is `tool_execution_start` / `_end`.
 *  - Codex / ACP: only ever emit tool_use openers (their terminal-state spans
 *    classify as tool_use, never tool_result), so the content-block scan returns
 *    `other` and the index's first-seen-is-opener fallback files them correctly.
 */
export type SpanRole = 'opener' | 'result' | 'other'

/** Claude/Anthropic: a tool_use content block marks an opener, a tool_result block a result. */
function blockSpanRole(parsed: ParsedMessageContent): SpanRole {
  const blocks = getMessageContent(parsed.parentObject ?? undefined)
  if (!blocks)
    return 'other'
  // Scan every block before deciding and let the `tool_use` opener signal win:
  // a message that carries BOTH a tool_use and a tool_result block still files as
  // the opener (it holds the tool input to render). Early-returning on the first
  // tool_result would mis-bucket such a message as a result, dropping its input.
  let hasToolUse = false
  let hasToolResult = false
  for (const b of blocks) {
    if (!isObject(b))
      continue
    if (b.type === 'tool_use')
      hasToolUse = true
    else if (b.type === 'tool_result')
      hasToolResult = true
  }
  return hasToolUse ? 'opener' : hasToolResult ? 'result' : 'other'
}

/**
 * Pi: the flat envelope `type` discriminates the start (opener) from the end
 * (result). Pi's `tool_execution_end` carries no Anthropic content blocks, so the
 * content-block scan would mis-bucket it as `other` -- routing by `type` files it
 * as a result regardless of arrival order.
 */
function piSpanRole(parsed: ParsedMessageContent): SpanRole {
  const type = pickString(parsed.parentObject, 'type')
  if (type === PI_EVENT.ToolExecutionStart)
    return 'opener'
  if (type === PI_EVENT.ToolExecutionEnd)
    return 'result'
  return 'other'
}

export function spanRole(provider: AgentProvider | undefined, parsed: ParsedMessageContent): SpanRole {
  if (provider === AgentProvider.PI)
    return piSpanRole(parsed)
  return blockSpanRole(parsed)
}

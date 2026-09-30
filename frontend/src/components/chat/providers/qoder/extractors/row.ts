import type { McpContentItem } from '../../../model/mcpToolCall'
import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { isObject, pickString } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { parseMcpContentItem } from '../../../model/mcpToolCall'
import { toolCallRow } from '../../../model/row'
import { anthropicBlock, anthropicBlocks, anthropicBlockText, anthropicToolCall } from './toolCommon'

/**
 * Read one Qoder row into the shared row model.
 *
 * The classification already read the frame's `type` and its content block, so
 * the work here turns the row into the neutral shape.
 */
export function qoderExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed, span } = input
  const payload = parsed.parentObject
  if (!payload || !isObject(payload))
    return null

  switch (category.kind) {
    case 'assistant_text':
      return textRow(payload, 'text', 'assistant-text')
    case 'assistant_thinking':
      return textRow(payload, 'thinking', 'assistant-thinking')
    case 'tool_use':
    case 'tool_result':
      return toolSpanRow(payload, span)
    case 'user_content':
      return leapmuxUserRow(payload)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(payload)
    default:
      return null
  }
}

function textRow(
  payload: Record<string, unknown>,
  blockType: string,
  kind: 'assistant-text' | 'assistant-thinking',
): ChatRow {
  const text = anthropicBlocks(payload, blockType)
    .map(block => anthropicBlockText(block, blockType === 'thinking' ? 'thinking' : 'text'))
    .join('')
  return text ? { kind, text } : { kind: 'hidden' }
}

function toolSpanRow(
  payload: Record<string, unknown>,
  span: RowExtractionInput['span'],
): ChatRow | null {
  const use = anthropicBlock(payload, 'tool_use')
  const result = anthropicBlock(payload, 'tool_result')
  // A result row states no tool name of its own; the span's request does.
  const requestUse = span.request ? anthropicBlock(span.request.parentObject ?? {}, 'tool_use') : undefined
  const callId = String(use?.id ?? result?.tool_use_id ?? '')
  const toolName = String(use?.name ?? requestUse?.name ?? '')
  const args = (use?.input && isObject(use.input) ? use.input : {}) as Record<string, unknown>
  const content = result ? resultContent(result) : { text: '', ordered: [] }
  const isError = result?.is_error === true

  const role = span.role === 'result' || (span.role === 'other' && !use) ? 'result' : 'request'
  const call = anthropicToolCall({
    callId,
    toolName,
    args,
    resultText: content.text,
    resultContent: content.ordered,
    isError,
    lifecycle: {
      frameStatus: result ? 'completed' : 'in_progress',
      providerOutcome: isError ? 'failed' : null,
      retainedOutcome: null,
      rowFinal: result !== undefined,
      resultFrameLanded: result !== undefined,
    },
  })
  return toolCallRow(call, role, span.visibleRows)
}

// A tool_result is a string or a list of content blocks. Keep the old text
// body for text-only results. A mixed result stays in wire order as content.
function resultContent(result: Record<string, unknown>): { text: string, ordered: McpContentItem[] } {
  const content = result.content
  if (typeof content === 'string')
    return { text: content, ordered: [] }
  if (Array.isArray(content)) {
    const blocks = content.filter(isObject)
    const ordered = blocks.map(parseMcpContentItem)
    if (ordered.some(item => item.type === 'image'))
      return { text: '', ordered }
    const text = blocks
      .filter(item => pickString(item, 'type') === 'text')
      .map(item => anthropicBlockText(item, 'text'))
      .join('')
    return { text, ordered: [] }
  }
  return { text: '', ordered: [] }
}

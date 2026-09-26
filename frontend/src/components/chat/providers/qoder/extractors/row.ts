import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { isObject, pickString } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
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
  const resultText = result ? resultContentText(result) : ''
  const isError = result?.is_error === true

  const role = span.role === 'result' || (span.role === 'other' && !use) ? 'result' : 'request'
  const call = anthropicToolCall({
    callId,
    toolName,
    args,
    resultText,
    isError,
    lifecycle: {
      frameStatus: result ? 'completed' : 'in_progress',
      providerOutcome: null,
      retainedOutcome: null,
      rowFinal: result !== undefined,
      resultFrameLanded: result !== undefined,
    },
  })
  return toolCallRow(call, role, span.visibleRows)
}

// A tool_result `content` is a string or a list of text blocks.
function resultContentText(result: Record<string, unknown>): string {
  const content = result.content
  if (typeof content === 'string')
    return content
  if (Array.isArray(content)) {
    return content
      .filter(item => isObject(item) && pickString(item, 'type') === 'text')
      .map(item => anthropicBlockText(item, 'text'))
      .join('')
  }
  return ''
}

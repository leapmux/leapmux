import type { CommandExit } from '../../../model/commandResult'
import type { McpContentItem } from '../../../model/mcpToolCall'
import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { parseMcpContentItem } from '../../../model/mcpToolCall'
import { toolCallRow } from '../../../model/row'
import { retainedOutcome } from '../../registry'
import { anthropicBlock, anthropicBlocks, anthropicBlockText, anthropicToolCall } from './toolCommon'

/**
 * Read one Qoder row into the shared row model.
 *
 * The classification already read the frame's `type` and its content block, so
 * the work here turns the row into the neutral shape.
 */
export function qoderExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed } = input
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
      if (input.span.role === 'none')
        return null
      return toolSpanRow(payload, input)
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
  input: RowExtractionInput,
): ChatRow | null {
  const { span } = input
  const explicitId = input.spanId || undefined
  const use = anthropicBlock(payload, 'tool_use', explicitId)
  const ownResult = anthropicBlock(payload, 'tool_result', explicitId)
  if (!use && !ownResult)
    return null
  const callId = String(use?.id ?? ownResult?.tool_use_id ?? '')
  if (!callId)
    return null
  const requestPayload = matchingSessionPayload(payload, span.request?.parentObject)
  const resultPayload = ownResult ? payload : matchingSessionPayload(payload, span.result?.parentObject)
  const requestUse = requestPayload ? anthropicBlock(requestPayload, 'tool_use', callId) : undefined
  const result = ownResult ?? (resultPayload ? anthropicBlock(resultPayload, 'tool_result', callId) : undefined)
  const toolName = String(use?.name ?? requestUse?.name ?? input.spanType ?? '')
  const nativeInput = use?.input ?? requestUse?.input
  const args = isObject(nativeInput) ? nativeInput : {}
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
    commandExit: qoderCommandExit(resultPayload, isError),
    lifecycle: {
      frameStatus: result ? 'completed' : 'in_progress',
      providerOutcome: isError ? 'failed' : null,
      retainedOutcome: retainedOutcome(input.completion ?? input.resolved.completion),
      rowFinal: result !== undefined,
      resultFrameLanded: result !== undefined,
    },
  })
  return toolCallRow(call, role, span.visibleRows)
}

/** Reject a paired frame that declares another native session. */
function matchingSessionPayload(payload: Record<string, unknown>, candidate: Record<string, unknown> | undefined): Record<string, unknown> | undefined {
  if (!candidate)
    return undefined
  const own = pickString(payload, 'session_id')
  const other = pickString(candidate, 'session_id')
  return own && other && own !== other ? undefined : candidate
}

/** Read the command's own exit metadata without guessing from output text. */
function qoderCommandExit(payload: Record<string, unknown> | undefined, failed: boolean): CommandExit {
  const result = pickObject(payload, 'tool_use_result')
  const code = result?.exitCode
  if (typeof code === 'number' && Number.isSafeInteger(code))
    return { exitCode: code }
  if (typeof result?.signal === 'string' && result.signal)
    return { signal: result.signal }
  return failed ? { failed: true } : {}
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

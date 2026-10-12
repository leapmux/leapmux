import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickString } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { retainedOutcome, retainedRowIsFinal } from '../../registry'
import { storedFunctionArgs, storedFunctionCallID, storedFunctionFailed, storedFunctionOutputText } from '../storedFunction'
import { codebuddyCommandOutput } from './execute'
import { anthropicBlock, anthropicBlocks, anthropicBlockText, codebuddyCommandExit, codebuddyToolCall } from './toolCommon'

/**
 * Read one CodeBuddy row into the shared row model.
 *
 * Classification already read the frame's type. Extraction reads live frames
 * and stored Workflow child records into neutral rows.
 */
export function codebuddyExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed, span } = input
  const payload = parsed.parentObject
  if (!payload || !isObject(payload))
    return null

  switch (category.kind) {
    case 'assistant_text':
      if (pickString(payload, 'type') === 'message')
        return storedAssistantTextRow(payload)
      return textRow(payload, 'text', 'assistant-text')
    case 'assistant_thinking':
      return textRow(payload, 'thinking', 'assistant-thinking')
    case 'tool_use':
    case 'tool_result': {
      if (span.role === 'none')
        return null
      const type = pickString(payload, 'type')
      return type === 'function_call' || type === 'function_call_output' || type === 'function_call_result'
        ? storedFunctionToolRow(payload, span, input.completion)
        : toolSpanRow(payload, span, input.completion)
    }
    case 'user_content':
      return leapmuxUserRow(payload)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(payload)
    default:
      return null
  }
}

function storedAssistantTextRow(payload: Record<string, unknown>): ChatRow {
  if (pickString(payload, 'role') !== 'assistant' || !Array.isArray(payload.content))
    return { kind: 'hidden' }
  const text = payload.content
    .filter(block => isObject(block) && pickString(block, 'type') === 'output_text')
    .map(block => isObject(block) ? pickString(block, 'text') ?? '' : '')
    .join('')
  return text ? { kind: 'assistant-text', text } : { kind: 'hidden' }
}

/** Keep a stored native function record in the same tool span as its partner. */
function storedFunctionToolRow(payload: Record<string, unknown>, span: RowExtractionInput['span'], completion: MessageCompletion | undefined): ChatRow {
  const isResult = pickString(payload, 'type') !== 'function_call'
  const requestPayload = span.request?.parentObject
  const callId = storedFunctionCallID(payload) ?? ''
  const pairedRequest = callId !== '' && isObject(requestPayload) && storedFunctionCallID(requestPayload) === callId
  const request = isResult && pairedRequest ? requestPayload : payload
  const toolName = pickString(request, 'name') || 'Tool'
  const args = storedFunctionArgs(request.arguments)
  const resultText = isResult ? storedFunctionOutputText(payload.output) : ''
  const isError = isResult && storedFunctionFailed(payload)
  const call = codebuddyToolCall({
    callId,
    toolName,
    args,
    resultText,
    isError,
    lifecycle: {
      frameStatus: isResult ? 'completed' : 'in_progress',
      providerOutcome: isError ? 'failed' : null,
      retainedOutcome: retainedOutcome(completion),
      rowFinal: isResult || retainedRowIsFinal(completion),
      resultFrameLanded: isResult,
    },
  })
  return toolCallRow(call, isResult ? 'result' : 'request', span.visibleRows)
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
  completion: MessageCompletion | undefined,
): ChatRow | null {
  const use = anthropicBlock(payload, 'tool_use')
  const result = anthropicBlock(payload, 'tool_result')
  // A result row states no tool name of its own; the span's request does.
  const spanRequestUse = span.request ? anthropicBlock(span.request.parentObject ?? {}, 'tool_use') : undefined
  const callId = pickString(use, 'id', undefined) ?? pickString(result, 'tool_use_id')
  const requestUse = callId !== '' && spanRequestUse && spanRequestUse.id === callId ? spanRequestUse : undefined
  const toolName = String(use?.name ?? requestUse?.name ?? '')
  const input = use?.input ?? requestUse?.input
  const args = isObject(input) ? input : {}
  const resultText = result ? resultContentText(result) : ''
  const isError = result?.is_error === true
  const commandExit = codebuddyCommandExit(result)
  const commandOutput = codebuddyCommandOutput(result, resultText)

  const role = span.role === 'result' || (span.role === 'other' && !use) ? 'result' : 'request'
  const call = codebuddyToolCall({
    callId,
    toolName,
    args,
    resultText,
    isError,
    ...(commandExit === undefined ? {} : { commandExit }),
    ...(commandOutput === undefined ? {} : { commandOutput }),
    lifecycle: {
      frameStatus: result ? 'completed' : 'in_progress',
      providerOutcome: null,
      retainedOutcome: retainedOutcome(completion),
      rowFinal: result !== undefined || retainedRowIsFinal(completion),
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

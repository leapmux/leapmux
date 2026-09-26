import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { DROID_NOTIFICATION_FIELD } from '~/generated/contracts/droid-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { leapmuxUserRow } from '../../../leapmuxRows'
import { createToolCall } from '../../../model/createToolCall'
import { toolCallRow } from '../../../model/row'
import { retainedRowIsFinal } from '../../registry'
import { droidToolKind } from '../toolKinds'

/**
 * Read one Factory Droid row into the shared row model.
 */
export function droidExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed, span } = input
  const payload = parsed.parentObject
  switch (category.kind) {
    case 'assistant_text':
      return { kind: 'assistant-text', text: pickString(payload, 'text') || '' }
    case 'assistant_thinking':
      return { kind: 'assistant-thinking', text: pickString(payload, 'text') || '' }
    case 'tool_use':
    case 'tool_result':
      return droidToolRow(payload, span, parsed.completion, category.kind === 'tool_result')
    case 'user_content':
      return leapmuxUserRow(payload)
    case 'result_divider':
      return { kind: 'divider', divider: { label: 'Turn ended' } }
    default:
      return null
  }
}

/** The lifecycle every Droid tool row shares. */
function droidLifecycle(
  completion: RowExtractionInput['resolved']['completion'],
  isError: boolean,
  isResult: boolean,
  resultText: string | undefined,
) {
  return {
    frameStatus: 'unstated' as const,
    providerOutcome: isError ? ('failed' as const) : null,
    retainedOutcome: retainedRowIsFinal(completion) ? ('succeeded' as const) : null,
    rowFinal: isResult,
    resultFrameLanded: resultText !== undefined,
  }
}

/** The row one tool call becomes, with both span sides resolved. */
function droidToolRow(
  payload: Record<string, unknown> | null | undefined,
  span: RowExtractionInput['span'],
  completion: RowExtractionInput['resolved']['completion'],
  isResult: boolean,
): ChatRow | null {
  if (!isObject(payload))
    return null
  const toolUse = pickObject(payload, DROID_NOTIFICATION_FIELD.ToolUse) ?? payload
  const name = pickString(toolUse, 'name') || pickString(toolUse, DROID_NOTIFICATION_FIELD.ToolName) || pickString(payload, DROID_NOTIFICATION_FIELD.ToolName) || 'Tool'
  const id = pickString(toolUse, 'id') || pickString(payload, DROID_NOTIFICATION_FIELD.ToolUseID)
  // A result frame states no input of its own. Fall back to the request side of
  // the span, which carries the call's arguments.
  const requestPayload = span.request?.parentObject
  const requestToolUse = isObject(requestPayload) ? (pickObject(requestPayload, DROID_NOTIFICATION_FIELD.ToolUse) ?? requestPayload) : undefined
  const args = pickObject(toolUse, 'input') ?? (requestToolUse ? pickObject(requestToolUse, 'input') : undefined) ?? {}
  const isError = Boolean(payload[DROID_NOTIFICATION_FIELD.IsError])
  const content = payload[DROID_NOTIFICATION_FIELD.Content]
  const resultText = content !== undefined ? (typeof content === 'string' ? content : JSON.stringify(content)) : undefined

  const declared = droidToolKind(name)
  const kind = declared === 'unspecified' ? 'other' : declared
  const lifecycle = droidLifecycle(completion, isError, isResult, resultText)
  const envelope = { id, name, lifecycle }
  // A result frame states no input of its own. The request row draws the diff;
  // the result row keeps the outcome as prose.
  const hasInput = Object.keys(args).length > 0

  // A file-change tool with its arguments draws the diff on the request row.
  // `filePath` is never blank: `createToolCall` degrades a file-change call
  // with a blank path, and the degraded row draws no diff at all.
  if ((kind === 'edit' || kind === 'write') && hasInput && !isResult) {
    const change = {
      filePath: (pickString(args, 'file_path') || pickString(args, 'path') || 'file').trim() || 'file',
      oldStr: pickString(args, 'old_string') || pickString(args, 'old_str'),
      newStr: pickString(args, 'new_string') || pickString(args, 'new_str') || pickString(args, 'content'),
    }
    const call = createToolCall(envelope, {
      kind,
      name,
      request: { changes: [change] },
      ...(resultText !== undefined ? { result: { changes: [change] } } : {}),
    })
    return toolCallRow(call, 'request', span.visibleRows)
  }

  // Every other row keeps the generic card: a raw-args request is not a typed
  // one, and a result frame's request row already drew whatever it had.
  const call = createToolCall(envelope, {
    kind: hasInput && !isResult ? kind : 'other',
    name,
    request: hasInput && !isResult ? args : { args },
    // Only a result frame states an outcome. A request frame with a result
    // trips the `result-before-the-call-finished` fault and degrades.
    ...(isResult || resultText !== undefined
      ? { result: { content: resultText !== undefined ? [{ type: 'text' as const, text: resultText }] : [] } }
      : {}),
  })
  return toolCallRow(call, isResult ? 'result' : 'request', span.visibleRows)
}

import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { LETTA_DELTA_FIELD } from '~/generated/contracts/letta-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { leapmuxUserRow } from '../../../leapmuxRows'
import { createToolCall } from '../../../model/createToolCall'
import { toolCallRow } from '../../../model/row'
import { retainedRowIsFinal } from '../../registry'
import { lettaToolKind } from '../toolKinds'

/**
 * Read one Letta Code row into the shared row model.
 */
export function lettaExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed, span } = input
  const payload = parsed.parentObject
  switch (category.kind) {
    case 'assistant_text':
      return { kind: 'assistant-text', text: pickString(payload, 'text') || '' }
    case 'assistant_thinking':
      return { kind: 'assistant-thinking', text: pickString(payload, 'text') || '' }
    case 'tool_use':
    case 'tool_result':
      return lettaToolRow(payload, span, parsed.completion, category.kind === 'tool_result')
    case 'user_content':
      return leapmuxUserRow(payload)
    case 'result_divider':
      return { kind: 'divider', divider: { label: 'Turn ended' } }
    default:
      return null
  }
}

/** The lifecycle every Letta tool row shares. */
function lettaLifecycle(
  completion: RowExtractionInput['resolved']['completion'],
  isResult: boolean,
  resultText: string | undefined,
) {
  return {
    frameStatus: 'unstated' as const,
    providerOutcome: null,
    retainedOutcome: retainedRowIsFinal(completion) ? ('succeeded' as const) : null,
    rowFinal: isResult,
    resultFrameLanded: resultText !== undefined,
  }
}

/** The row one tool call becomes, with both span sides resolved. */
function lettaToolRow(
  payload: Record<string, unknown> | null | undefined,
  span: RowExtractionInput['span'],
  completion: RowExtractionInput['resolved']['completion'],
  isResult: boolean,
): ChatRow | null {
  if (!isObject(payload))
    return null
  // The worker persists the stream_delta's own payload object. A raw frame
  // wraps it under `payload`, so normalize before reading.
  const nested = pickObject(payload, 'payload')
  const source = nested && Object.keys(nested).length > 0 ? nested : payload
  const name = pickString(source, LETTA_DELTA_FIELD.ToolName) || 'Tool'
  const id = pickString(source, LETTA_DELTA_FIELD.ToolCallID)
  // A result frame states no input of its own. Fall back to the request side of
  // the span, which carries the call's arguments.
  const requestPayload = span.request?.parentObject
  const requestNested = isObject(requestPayload) ? pickObject(requestPayload, 'payload') : undefined
  const requestSource = requestNested && Object.keys(requestNested).length > 0 ? requestNested : requestPayload
  const args = pickObject(source, LETTA_DELTA_FIELD.ToolInput) ?? (isObject(requestSource) ? pickObject(requestSource, LETTA_DELTA_FIELD.ToolInput) : undefined) ?? {}
  const toolReturn = source[LETTA_DELTA_FIELD.ToolReturn]
  const resultText = toolReturn !== undefined
    ? (typeof toolReturn === 'string' ? toolReturn : JSON.stringify(toolReturn))
    : undefined

  const declared = lettaToolKind(name)
  const kind = declared === 'unspecified' ? 'other' : declared
  const lifecycle = lettaLifecycle(completion, isResult, resultText)
  const envelope = { id, name, lifecycle }
  // A result frame states no input of its own. The request row draws the diff;
  // the result row keeps the outcome as prose.
  const hasInput = Object.keys(args).length > 0

  // A file-change tool with its arguments draws the diff on the request row.
  // `filePath` is never blank: `createToolCall` degrades a file-change call
  // with a blank path, and the degraded row draws no diff at all.
  if ((kind === 'edit' || kind === 'write') && hasInput && !isResult) {
    const change = {
      filePath: (pickString(args, 'path') || pickString(args, 'file_path') || 'file').trim() || 'file',
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

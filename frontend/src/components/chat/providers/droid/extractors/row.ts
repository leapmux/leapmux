import type { McpContentItem } from '../../../model/mcpToolCall'
import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { DROID_NOTIFICATION_FIELD } from '~/generated/contracts/droid-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { leapmuxUserRow } from '../../../leapmuxRows'
import { createToolCall } from '../../../model/createToolCall'
import { parseMcpContentItem } from '../../../model/mcpToolCall'
import { toolCallRow } from '../../../model/row'
import { rawTodosToItems } from '../../../normalizers/todo'
import { toolRequestFor } from '../../defaultToolRequests'
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
  hasResultContent: boolean,
) {
  return {
    frameStatus: 'unstated' as const,
    providerOutcome: isError ? ('failed' as const) : null,
    retainedOutcome: retainedRowIsFinal(completion) ? ('succeeded' as const) : null,
    rowFinal: isResult,
    resultFrameLanded: hasResultContent,
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
  const resultContent = droidResultContent(content)

  const declared = droidToolKind(name)
  const kind = declared === 'unspecified' ? 'other' : declared
  const lifecycle = droidLifecycle(completion, isError, isResult, content !== undefined)
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
      ...(content !== undefined ? { result: { changes: [change] } } : {}),
    })
    return toolCallRow(call, 'request', span.visibleRows)
  }

  // Every other row keeps the generic card: a raw-args request is not a typed
  // one, and a result frame's request row already drew whatever it had. Each
  // declared kind receives its typed request before its renderer reads it.
  const requestKind = hasInput && !isResult ? kind : 'other'
  const request = requestKind === 'todo'
    ? { items: rawTodosToItems(args.todos) }
    : toolRequestFor(requestKind, args, {}, {})
  const call = createToolCall(envelope, {
    kind: requestKind,
    name,
    request,
    // Only a result frame states an outcome. A request frame with a result
    // trips the `result-before-the-call-finished` fault and degrades.
    ...(isResult || content !== undefined
      ? { result: { content: resultContent } }
      : {}),
  })
  return toolCallRow(call, isResult ? 'result' : 'request', span.visibleRows)
}

/** Read Droid's result blocks without turning a picture into JSON text. */
function droidResultContent(content: unknown): McpContentItem[] {
  if (typeof content === 'string')
    return [{ type: 'text', text: content }]
  if (!Array.isArray(content)) {
    const text = JSON.stringify(content)
    return text === undefined ? [] : [{ type: 'text', text }]
  }
  return content.map((block): McpContentItem => {
    if (isObject(block) && block.type === 'image') {
      const source = pickObject(block, 'source')
      if (source?.type === 'base64') {
        const data = pickString(source, 'data', undefined)
        const mimeType = pickString(source, 'mediaType', undefined)
        if (data && mimeType)
          return { type: 'image', source: { data, mimeType } }
      }
    }
    return parseMcpContentItem(block)
  })
}

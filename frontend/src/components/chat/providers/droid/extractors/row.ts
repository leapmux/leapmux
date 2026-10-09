import type { CommandResult } from '../../../model/commandResult'
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
import { retainedOutcome, retainedRowIsFinal } from '../../registry'
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
    retainedOutcome: retainedOutcome(completion),
    rowFinal: isResult || retainedRowIsFinal(completion),
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
  const id = pickString(toolUse, 'id') || pickString(payload, DROID_NOTIFICATION_FIELD.ToolUseID)
  // A native result can omit its tool name and arguments.
  // Use its request only when the native call IDs match.
  const requestPayload = span.request?.parentObject
  const requestCandidate = isObject(requestPayload) ? (pickObject(requestPayload, DROID_NOTIFICATION_FIELD.ToolUse) ?? requestPayload) : undefined
  const requestID = pickString(requestCandidate, 'id') || pickString(requestPayload, DROID_NOTIFICATION_FIELD.ToolUseID)
  const requestToolUse = id !== '' && requestID === id ? requestCandidate : undefined
  const name = pickString(toolUse, 'name') || pickString(toolUse, DROID_NOTIFICATION_FIELD.ToolName)
    || pickString(payload, DROID_NOTIFICATION_FIELD.ToolName)
    || pickString(requestToolUse, 'name') || pickString(requestToolUse, DROID_NOTIFICATION_FIELD.ToolName) || 'Tool'
  const args = pickObject(toolUse, 'input') ?? (requestToolUse ? pickObject(requestToolUse, 'input') : undefined) ?? {}
  const isError = Boolean(payload[DROID_NOTIFICATION_FIELD.IsError])
  const content = payload[DROID_NOTIFICATION_FIELD.Content]
  const resultContent = droidResultContent(content)

  const declared = droidToolKind(name)
  const kind = declared === 'unspecified' ? 'other' : declared
  const lifecycle = droidLifecycle(completion, isError, isResult, content !== undefined)
  const envelope = { id, name, lifecycle }
  // The request row draws a file diff. The result row keeps its outcome as prose.
  const hasInput = Object.keys(args).length > 0

  // A file-change request with arguments draws its diff.
  // createToolCall degrades a file-change call with an empty filePath, so that call draws no diff.
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

  // A command result states how the command ended, which only the command body draws.
  const commandText = kind === 'execute' && hasInput && isResult && content !== undefined ? droidCommandText(resultContent) : undefined
  if (commandText !== undefined) {
    const call = createToolCall(envelope, {
      kind: 'execute',
      name,
      request: toolRequestFor('execute', args, {}, {}),
      result: { commands: [droidCommandResult(commandText)], unresolvedTerminals: [] },
    })
    return toolCallRow(call, 'result', span.visibleRows)
  }

  // Other result rows keep the generic card because their request row already shows the typed input.
  // A request row receives the typed request for its declared kind.
  const requestKind = hasInput && !isResult ? kind : 'other'
  const request = requestKind === 'todo'
    ? { items: rawTodosToItems(args.todos) }
    : toolRequestFor(requestKind, args, {}, {})
  const call = createToolCall(envelope, {
    kind: requestKind,
    name,
    request,
    // Retained finality supplies no native result content.
    // Keep an actual empty result, and keep absent content absent.
    ...(content !== undefined
      ? { result: { content: resultContent } }
      : {}),
  })
  return toolCallRow(call, isResult ? 'result' : 'request', span.visibleRows)
}

/**
 * The trailer that Droid 0.233.0 writes after the output of every finished command. The
 * Droid binary reads the code with the same pattern.
 */
const DROID_EXIT_TRAILER = /\[Process exited with code (-?\d+)\]\s*$/

/** The text of a command result, or undefined for a result that holds anything but text. */
function droidCommandText(content: readonly McpContentItem[]): string | undefined {
  let text = ''
  for (const item of content) {
    if (item.type !== 'text')
      return undefined
    text += item.text
  }
  return text
}

/**
 * One command and the code of its native trailer.
 *
 * The row shows command output without the native status trailer.
 * A matching failure preamble states the same exit, so the row omits it also.
 */
function droidCommandResult(text: string): CommandResult {
  const trailer = DROID_EXIT_TRAILER.exec(text)
  const exitCode = trailer ? Number(trailer[1]) : Number.NaN
  if (!trailer || !Number.isSafeInteger(exitCode))
    return { output: text }
  let output = text.slice(0, trailer.index).replace(/\r?\n\r?\n$/u, '')
  const failure = `Error: Command failed (exit code: ${exitCode})\n`
  if (exitCode !== 0 && output.startsWith(failure))
    output = output.slice(failure.length)
  return { output, exitCode }
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

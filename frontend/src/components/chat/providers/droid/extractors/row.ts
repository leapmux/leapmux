import type { CommandResult } from '../../../model/commandResult'
import type { McpContentItem } from '../../../model/mcpToolCall'
import type { ChatRow } from '../../../model/row'
import type { ToolCallSpecVariant } from '../../../model/toolCall'
import type { ToolKind } from '../../../model/toolKind'
import type { ToolRequestByKind } from '../../../model/tools'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { DROID_NOTIFICATION_FIELD } from '~/generated/contracts/droid-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { leapmuxUserRow } from '../../../leapmuxRows'
import { createToolCall } from '../../../model/createToolCall'
import { parseMcpContentItem } from '../../../model/mcpToolCall'
import { toolCallRow } from '../../../model/row'
import { failedResult, isGenericKind, unparsedResult } from '../../../model/toolCall'
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
      return {
        kind: 'divider',
        divider: { label: 'Turn ended' },
      }
    default:
      return null
  }
}

/**
 * The lifecycle every Droid tool row shares. `rowFinal` states whether the row is the
 * final row of its span (see droidToolRow).
 */
function droidLifecycle(
  completion: RowExtractionInput['resolved']['completion'],
  isError: boolean,
  rowFinal: boolean,
  hasResultContent: boolean,
) {
  return {
    frameStatus: 'unstated' as const,
    providerOutcome: isError ? ('failed' as const) : null,
    retainedOutcome: retainedOutcome(completion),
    rowFinal,
    resultFrameLanded: hasResultContent,
  }
}

/**
 * The row one tool call becomes, from its own notification and the earlier request.
 *
 * A request row reads its own notification only. A result row reads the name and the
 * arguments of its request when its own notification omits them. Both rows carry the
 * kind that the tool declares and the request that its arguments state, so the header
 * reads the same on both. Only the result row carries the result.
 */
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
  const requestCandidate = isObject(requestPayload)
    ? (pickObject(requestPayload, DROID_NOTIFICATION_FIELD.ToolUse) ?? requestPayload)
    : undefined
  const requestID = pickString(requestCandidate, 'id') || pickString(requestPayload, DROID_NOTIFICATION_FIELD.ToolUseID)
  const requestToolUse = id !== '' && requestID === id
    ? requestCandidate
    : undefined
  const name = pickString(toolUse, 'name') || pickString(toolUse, DROID_NOTIFICATION_FIELD.ToolName)
    || pickString(payload, DROID_NOTIFICATION_FIELD.ToolName)
    || pickString(requestToolUse, 'name') || pickString(requestToolUse, DROID_NOTIFICATION_FIELD.ToolName) || 'Tool'
  const args = pickObject(toolUse, 'input') ?? (requestToolUse
    ? pickObject(requestToolUse, 'input')
    : undefined) ?? {}
  const isError = Boolean(payload[DROID_NOTIFICATION_FIELD.IsError])
  const content = payload[DROID_NOTIFICATION_FIELD.Content]
  // A request notification states no content, and a request row never carries a result.
  // An empty content is still a result. A result with retained finality and no content
  // has none.
  const resultContent = isResult && content !== undefined
    ? droidResultContent(content)
    : undefined

  // A call that states no arguments keeps the generic card: the declared kind has
  // nothing to read.
  const declared = droidToolKind(name)
  const kind = declared === 'unspecified' || Object.keys(args).length === 0
    ? 'other'
    : declared
  // A `tool_call` notification that LeapMux stored with a completion is the final row
  // of its span (see retainedRowIsFinal), as for every provider. The Droid Worker stores
  // each `tool_call` once with no completion today; this keeps a stored completion from
  // leaving an open request row with a pending row that never leaves.
  const rowFinal = isResult || retainedRowIsFinal(completion)
  const call = createToolCall({
    id,
    name,
    lifecycle: droidLifecycle(completion, isError, rowFinal, resultContent !== undefined),
  }, {
    kind,
    name,
    request: droidRequest(kind, args),
    ...(resultContent === undefined
      ? {}
      : droidResult(kind, resultContent, isError)),
  })
  return toolCallRow(call, rowFinal
    ? 'result'
    : 'request', span.visibleRows)
}

/** The request that a call's arguments state for its kind. */
function droidRequest(kind: ToolKind, args: Record<string, unknown>): ToolRequestByKind[ToolKind] {
  switch (kind) {
    case 'edit':
    case 'write':
      // createToolCall degrades a file-change call with an empty filePath, so the
      // change states a placeholder file when the arguments state none.
      return {
        changes: [
          {
            filePath: (pickString(args, 'file_path') || pickString(args, 'path') || 'file').trim() || 'file',
            oldStr: pickString(args, 'old_string') || pickString(args, 'old_str'),
            newStr: pickString(args, 'new_string') || pickString(args, 'new_str') || pickString(args, 'content'),
          },
        ],
      }
    case 'todo':
      return {
        items: rawTodosToItems(args.todos),
      }
    default:
      return toolRequestFor(kind, args, {}, {})
  }
}

/**
 * The result of one call from the native content of its result notification.
 *
 * - A command whose content is text alone draws the command result: its output and
 *   the code of its native trailer.
 * - The generic card keeps the native content blocks as they are.
 * - Every other kind keeps the native text as unparsed text. Content that holds any
 *   block but text, such as a picture, stays in its native order beside the call.
 */
function droidResult(kind: ToolKind, content: McpContentItem[], isError: boolean): Pick<ToolCallSpecVariant<ToolKind>, 'result' | 'extraContent'> {
  const commandText = kind === 'execute'
    ? droidContentText(content)
    : undefined
  if (commandText !== undefined) {
    return {
      result: {
        commands: [
          droidCommandResult(commandText),
        ],
        unresolvedTerminals: [],
      },
    }
  }
  if (isGenericKind(kind)) {
    return {
      result: { content },
    }
  }
  const ordered = content.some(item => item.type !== 'text')
  const text = ordered
    ? ''
    : droidContentText(content) ?? ''
  return {
    result: isError
      ? failedResult(text)
      : unparsedResult(text),
    ...(ordered
      ? { extraContent: content }
      : {}),
  }
}

/**
 * The trailer that Droid 0.233.0 writes after the output of every finished command. The
 * Droid binary reads the code with the same pattern.
 */
const DROID_EXIT_TRAILER = /\[Process exited with code (-?\d+)\]\s*$/

/** The text of a result, or undefined for a result that holds anything but text. */
function droidContentText(content: readonly McpContentItem[]): string | undefined {
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
  const exitCode = trailer
    ? Number(trailer[1])
    : Number.NaN
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
  if (typeof content === 'string') {
    return [
      { type: 'text', text: content },
    ]
  }
  if (!Array.isArray(content)) {
    const text = JSON.stringify(content)
    return text === undefined
      ? []
      : [
          { type: 'text', text },
        ]
  }
  return content.map((block): McpContentItem => {
    if (isObject(block) && block.type === 'image') {
      const source = pickObject(block, 'source')
      if (source?.type === 'base64') {
        const data = pickString(source, 'data', undefined)
        const mimeType = pickString(source, 'mediaType', undefined)
        if (data && mimeType) {
          return {
            type: 'image',
            source: { data, mimeType },
          }
        }
      }
    }
    return parseMcpContentItem(block)
  })
}

import type { ParsedMessageContent } from '~/lib/messageParser'
import { DIRAC_NATIVE_BLOCK as BLOCK, DIRAC_REQUEST_CONTEXT as CONTEXT, DIRAC_SUPPLEMENT_ROOT, DIRAC_RECORD_FIELD as FIELD, DIRAC_RECORD_KIND as KIND, DIRAC_RECORD_ROLE as ROLE } from '~/generated/contracts/dirac-protocol'
import { ASSEMBLED_MESSAGE } from '~/generated/contracts/worker-vocab'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { messageCompletionFromProto } from '../../assembledMessage'
import { resolveACPMessage } from '../acp/extractors/toolCall'

function nativeMarkdown(record: Record<string, unknown> | null | undefined) {
  const message = pickObject(record, FIELD.Message)
  const content = pickObject(message, 'content')
  if (record?.[FIELD.Type] !== KIND.Message || !pickString(message, 'id') || content?.type !== 'markdown'
    || typeof content.content !== 'string' || (content.role !== undefined && content.role !== 'assistant')
    || (content.isReasoning !== undefined && typeof content.isReasoning !== 'boolean')
    || record[FIELD.Role] !== undefined || record[FIELD.Declaration] !== undefined || record[FIELD.Result] !== undefined) {
    return undefined
  }
  const context = pickObject(record, FIELD.RequestContext)
  const taskletOutcome = pickObject(record, FIELD.TaskletOutcome)
  if (record[FIELD.TaskletOutcome] !== undefined && (!taskletOutcome || typeof taskletOutcome.kind !== 'string'))
    return undefined
  if (record[FIELD.RequestContext] !== undefined && (!context || !pickString(context, CONTEXT.MessageID)
    || !Number.isSafeInteger(context[CONTEXT.HistoryIndex]) || Number(context[CONTEXT.HistoryIndex]) < -1
    || !Number.isSafeInteger(message?.conversationHistoryIndex)
    || Number(context[CONTEXT.HistoryIndex]) + 1 !== message?.conversationHistoryIndex
    || JSON.stringify(context[CONTEXT.DeletedRange] ?? null) !== JSON.stringify(message?.conversationHistoryDeletedRange ?? null)
    || (context[CONTEXT.CancelReason] !== undefined && typeof context[CONTEXT.CancelReason] !== 'string')
    || (context[CONTEXT.StopReason] !== undefined && typeof context[CONTEXT.StopReason] !== 'string'))) {
    return undefined
  }
  return {
    id: pickString(message, 'id'),
    text: content.content,
    role: content.role,
    reasoning: content.isReasoning === true,
    requestId: pickString(context, CONTEXT.MessageID),
    cancelled: !!pickString(context, CONTEXT.CancelReason),
  }
}

function resolveDiracMarkdown(parsed: ParsedMessageContent, record: Record<string, unknown>): Record<string, unknown> | undefined {
  const original = nativeMarkdown(record)
  if (!original)
    return undefined
  const latest = pickObject(isObject(parsed.supplementalContent)
    ? parsed.supplementalContent
    : undefined, DIRAC_SUPPLEMENT_ROOT.Message)
  const candidate = nativeMarkdown(latest)
  const current = latest && candidate && candidate.id === original.id && candidate.role === original.role && candidate.reasoning === original.reasoning
    && (!original.requestId || candidate.requestId === original.requestId) && (!original.cancelled || candidate.cancelled)
    && latest[FIELD.SessionID] === record[FIELD.SessionID] && latest[FIELD.ParentSessionID] === record[FIELD.ParentSessionID]
    && latest[FIELD.TaskletID] === record[FIELD.TaskletID]
    ? candidate
    : original
  return {
    [ASSEMBLED_MESSAGE.FieldType]: ASSEMBLED_MESSAGE.Type,
    [ASSEMBLED_MESSAGE.FieldKind]: current.reasoning ? ASSEMBLED_MESSAGE.KindReasoning : ASSEMBLED_MESSAGE.KindText,
    [ASSEMBLED_MESSAGE.FieldText]: current.text,
    [ASSEMBLED_MESSAGE.FieldCompletion]: messageCompletionFromProto(parsed.completion) ?? ASSEMBLED_MESSAGE.CompletionComplete,
  }
}

/** Render a captured native tasklet block without changing its stored bytes. */
export function resolveDiracMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const record = parsed.parentObject
  if (record?.[FIELD.Type] !== KIND.Tool && record?.[FIELD.Type] !== KIND.Message)
    return resolveACPMessage(parsed)
  if (!parsed.agentSessionId || record[FIELD.SessionID] !== parsed.agentSessionId
    || !pickString(record, FIELD.ParentSessionID) || !pickString(record, FIELD.TaskletID)
    || record[FIELD.ParentSessionID] === parsed.agentSessionId) {
    return undefined
  }
  if (record[FIELD.Type] === KIND.Message)
    return resolveDiracMarkdown(parsed, record)
  if (record[FIELD.Message] !== undefined)
    return undefined
  const declaration = pickObject(record, FIELD.Declaration)
  const id = pickString(declaration, 'id')
  const name = pickString(declaration, 'name')
  if (!declaration || declaration.type !== BLOCK.ToolUse || !id || !name)
    return undefined
  const role = record[FIELD.Role]
  const result = pickObject(record, FIELD.Result)
  if ((role !== ROLE.Request && role !== ROLE.Result && role !== ROLE.Retained)
    || (role !== ROLE.Result && record[FIELD.Result] !== undefined)
    || (role === ROLE.Retained && messageCompletionFromProto(parsed.completion) !== ASSEMBLED_MESSAGE.CompletionFinished)
    || (role === ROLE.Result && (!result || result.type !== BLOCK.ToolResult || result.tool_use_id !== id))) {
    return undefined
  }
  const input = pickObject(declaration, 'input') ?? { input: declaration.input }
  const kind = name === 'read_file' || name === 'list_files'
    ? 'read'
    : name === 'execute_command'
      ? 'execute'
      : name === 'edit_file' || name === 'write_to_file'
        ? 'edit'
        : name === 'search_files'
          ? 'search'
          : 'other'
  const paths = Array.isArray(input.paths)
    ? input.paths.filter((path): path is string => typeof path === 'string' && path !== '')
    : []
  return {
    sessionUpdate: role === ROLE.Request
      ? 'tool_call'
      : 'tool_call_update',
    toolCallId: id,
    name,
    title: name,
    kind,
    status: role === ROLE.Request
      ? 'in_progress'
      : role === ROLE.Retained
        ? undefined
        : result?.is_error === true
          ? 'failed'
          : 'completed',
    rawInput: { ...input, tool: name },
    ...(paths.length > 0
      ? {
          locations: paths.map(path => ({ path })),
        }
      : {}),
    ...(result && Object.hasOwn(result, 'content')
      ? { rawOutput: result.content }
      : {}),
  }
}

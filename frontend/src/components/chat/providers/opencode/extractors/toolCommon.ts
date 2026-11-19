import type { ResolvedMessageContent } from '../../../rowExtractionTypes'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { OPENCODE_NATIVE_EVENT, OPENCODE_NATIVE_PART_TYPE, OPENCODE_NATIVE_TOOL_STATUS } from '~/generated/contracts/opencode-native-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { retainedRowIsFinal } from '../../registry'

interface OpenCodeToolFields {
  callId: string
  name: string
  status: string
  input: Record<string, unknown>
  metadata: Record<string, unknown>
  content: unknown[]
  error: Record<string, unknown> | null
}

/** A recovered part lacks the message and session IDs carried by a live event. */
export type OpenCodeToolRecord = OpenCodeToolFields & (
  | { wireForm: 'event', messageId: string, sessionId: string }
  | { wireForm: 'projection' }
)

/** Read a complete native call after supplemental display resolution. */
export function openCodeToolRecord(parsed: unknown): OpenCodeToolRecord | null {
  if (!isObject(parsed))
    return null
  if (parsed.type === OPENCODE_NATIVE_PART_TYPE.Tool) {
    const state = pickObject(parsed, 'state')
    if (typeof parsed.id !== 'string' || typeof parsed.name !== 'string' || !state || !isObject(state.input))
      return null
    const status = pickString(state, 'status')
    if (status !== OPENCODE_NATIVE_TOOL_STATUS.Running
      && status !== OPENCODE_NATIVE_TOOL_STATUS.Completed
      && status !== OPENCODE_NATIVE_TOOL_STATUS.Error) {
      return null
    }
    return {
      wireForm: 'projection',
      callId: parsed.id,
      name: parsed.name,
      status,
      input: state.input,
      metadata: pickObject(state, 'metadata') ?? {},
      content: Array.isArray(state.content)
        ? state.content
        : [],
      error: pickObject(state, 'error') ?? null,
    }
  }
  let status: string
  switch (parsed.type) {
    case OPENCODE_NATIVE_EVENT.ToolCalled:
    case OPENCODE_NATIVE_EVENT.ToolProgress:
      status = OPENCODE_NATIVE_TOOL_STATUS.Running
      break
    case OPENCODE_NATIVE_EVENT.ToolSuccess:
      status = OPENCODE_NATIVE_TOOL_STATUS.Completed
      break
    case OPENCODE_NATIVE_EVENT.ToolFailed:
      status = OPENCODE_NATIVE_TOOL_STATUS.Error
      break
    default:
      return null
  }
  const data = pickObject(parsed, 'data')
  if (!data || typeof data.id !== 'string' || typeof data.name !== 'string'
    || typeof data.assistantMessageID !== 'string' || !data.assistantMessageID
    || typeof data.sessionID !== 'string' || !data.sessionID || !isObject(data.input)) {
    return null
  }
  return {
    wireForm: 'event',
    callId: data.id,
    messageId: data.assistantMessageID,
    sessionId: data.sessionID,
    name: data.name,
    status,
    input: data.input,
    metadata: pickObject(data, 'metadata') ?? {},
    content: Array.isArray(data.content)
      ? data.content
      : [],
    error: pickObject(data, 'error') ?? null,
  }
}

/** Worker completion distinguishes the two retained rows of a recovered part. */
export function openCodeToolSpanRole(record: OpenCodeToolRecord, completion?: MessageCompletion): ToolSpanRole {
  if (retainedRowIsFinal(completion))
    return 'result'
  if (record.wireForm === 'projection')
    return 'request'
  return record.status === OPENCODE_NATIVE_TOOL_STATUS.Running
    ? 'request'
    : 'result'
}

/** Identify tool sides for the shared span index. */
export function openCodeSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const record = openCodeToolRecord(parsed.parentObject)
  return record
    ? openCodeToolSpanRole(record, parsed.completion)
    : 'none'
}

/** The span index supplies projected identity; live sides also verify their native scope. */
export function sameOpenCodeTool(left: OpenCodeToolRecord, right: OpenCodeToolRecord): boolean {
  return left.callId === right.callId
    && (left.wireForm !== 'event' || right.wireForm !== 'event'
      || (left.messageId === right.messageId && left.sessionId === right.sessionId))
}

/** Text content remains separate from files and unknown native content. */
export function openCodeToolText(record: OpenCodeToolRecord): string {
  return record.content.flatMap(item => isObject(item) && item.type === 'text' && typeof item.text === 'string'
    ? [item.text]
    : []).join('\n')
}

import type { MockModelRequestRecord } from './mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'
import { kiroCurrentUserInput } from './kiroSurface'
import { requestRows } from './modelRequestBody'

function objectItems(value: unknown): Record<string, unknown>[] {
  return Array.isArray(value) ? value.filter(isObject) : []
}

/** Where one protocol keeps the result entries of a request, and which field of an entry holds the content. */
interface ResultLocation {
  /** Every result entry of the request with this call ID. */
  entries: (request: MockModelRequestRecord, callId: string) => Record<string, unknown>[]
  /** The field of an entry that holds its native content. */
  content: string
}

const RESULT_LOCATIONS: Readonly<Record<MockModelRequestRecord['protocol'], ResultLocation>> = {
  'openai-chat-completions': {
    entries: (request, callId) => objectItems(requestRows(request.protocol, request.body))
      .filter(message => message.role === 'tool' && message.tool_call_id === callId),
    content: 'content',
  },
  'openai-responses': {
    entries: (request, callId) => objectItems(requestRows(request.protocol, request.body))
      .filter(item => (item.type === 'function_call_output' || item.type === 'custom_tool_call_output') && item.call_id === callId),
    content: 'output',
  },
  'google-generative-language': {
    entries: (request, callId) => objectItems(requestRows(request.protocol, request.body))
      .filter(message => message.role === 'user')
      .flatMap(message => objectItems(message.parts))
      .map(part => part.functionResponse)
      .filter(isObject)
      .filter(result => result.id === callId),
    content: 'response',
  },
  'anthropic-messages': {
    entries: (request, callId) => objectItems(requestRows(request.protocol, request.body))
      .filter(message => message.role === 'user')
      .flatMap(message => objectItems(message.content))
      .filter(block => block.type === 'tool_result' && block.tool_use_id === callId),
    content: 'content',
  },
  // Kiro's service carries the results of a turn in the context of its current user input.
  'aws-event-stream': {
    entries: (request, callId) => {
      const context = kiroCurrentUserInput(request.body)?.userInputMessageContext
      return objectItems(isObject(context) ? context.toolResults : undefined).filter(result => result.toolUseId === callId)
    },
    content: 'content',
  },
}

function recordedRequest(request: MockModelRequestRecord | undefined, callId: string): MockModelRequestRecord {
  if (!request)
    throw new Error(`No native model request exists for tool call ${callId}.`)
  return request
}

function onlyEntry(request: MockModelRequestRecord, callId: string): Record<string, unknown> {
  const entries = RESULT_LOCATIONS[request.protocol].entries(request, callId)
  if (entries.length !== 1)
    throw new Error(`The native request contains ${entries.length} results for ${callId}. Exactly one result is required.`)
  return entries[0]!
}

/**
 * Read the one native result entry for a tool call: the whole entry, so a provider reader can also read a field
 * beside the content, such as Kiro's `status`.
 */
export function nativeToolResultEntry(request: MockModelRequestRecord | undefined, callId: string): Record<string, unknown> {
  return onlyEntry(recordedRequest(request, callId), callId)
}

/** Read only the native content for one tool call. Preserve its original type for provider validation. */
export function nativeToolResultContent(request: MockModelRequestRecord | undefined, callId: string): unknown {
  const recorded = recordedRequest(request, callId)
  const content = onlyEntry(recorded, callId)[RESULT_LOCATIONS[recorded.protocol].content]
  if (content === undefined || content === null)
    throw new Error(`The native result for ${callId} has no content.`)
  return content
}

/** Serialize one exact native result. The call arguments can repeat every offered answer. */
export function nativeToolResult(request: MockModelRequestRecord | undefined, callId: string): string {
  const content = nativeToolResultContent(request, callId)
  return typeof content === 'string' ? content : JSON.stringify(content)
}

/** Whether the request carries a result for the call, in its protocol's result location. */
export function hasNativeToolResult(request: MockModelRequestRecord, callId: string): boolean {
  return RESULT_LOCATIONS[request.protocol].entries(request, callId).length > 0
}

/** One tool call that a model request carries in its history: the native ID, the tool name, and the arguments. */
export interface NativeToolCallRecord {
  id: string
  name: string
  /** The decoded arguments. A protocol that sends them as JSON text has them parsed. */
  arguments: unknown
}

/** A tool call before its arguments are decoded: `encoded` is JSON text, and `value` is a decoded value. */
type RawToolCall = { id: unknown, name: unknown } & ({ encoded: unknown } | { value: unknown })

/** Where one protocol keeps the tool calls of the model's earlier turns. */
const CALL_LOCATIONS: Readonly<Record<MockModelRequestRecord['protocol'], (request: MockModelRequestRecord) => RawToolCall[]>> = {
  'openai-chat-completions': request => objectItems(requestRows(request.protocol, request.body))
    .filter(message => message.role === 'assistant')
    .flatMap(message => objectItems(message.tool_calls))
    .map(call => ({ id: call.id, name: isObject(call.function) ? call.function.name : undefined, encoded: isObject(call.function) ? call.function.arguments : undefined })),
  'openai-responses': request => objectItems(requestRows(request.protocol, request.body)).flatMap((item): RawToolCall[] => {
    if (item.type === 'function_call')
      return [{ id: item.call_id, name: item.name, encoded: item.arguments }]
    // A custom tool takes free text, not JSON.
    if (item.type === 'custom_tool_call')
      return [{ id: item.call_id, name: item.name, value: item.input }]
    return []
  }),
  'google-generative-language': request => objectItems(requestRows(request.protocol, request.body))
    .filter(message => message.role === 'model')
    .flatMap(message => objectItems(message.parts))
    .map(part => part.functionCall)
    .filter(isObject)
    .map(call => ({ id: call.id, name: call.name, value: call.args })),
  'anthropic-messages': request => objectItems(requestRows(request.protocol, request.body))
    .filter(message => message.role === 'assistant')
    .flatMap(message => objectItems(message.content))
    .filter(block => block.type === 'tool_use')
    .map(block => ({ id: block.id, name: block.name, value: block.input })),
  // Kiro's service keeps the tool uses of earlier turns in the conversation history.
  'aws-event-stream': (request) => {
    const state = isObject(request.body) ? request.body.conversationState : undefined
    return objectItems(isObject(state) ? state.history : undefined)
      .map(entry => entry.assistantResponseMessage)
      .filter(isObject)
      .flatMap(message => objectItems(message.toolUses))
      .map(use => ({ id: use.toolUseId, name: use.name, value: use.input }))
  },
}

/**
 * Read the one tool call of a request that `matches` selects: a native call ID, or a test of the native ID for a
 * provider that rewrites the ID, such as Droid. `label` gives the call in a failure message. No match, more than one
 * match, a call with no name, and arguments that are not valid JSON each throw.
 */
export function nativeToolCallArguments(
  request: MockModelRequestRecord,
  matches: string | ((id: string) => boolean),
  label: string = typeof matches === 'string' ? matches : 'the selected call',
): NativeToolCallRecord {
  const selected = typeof matches === 'string' ? (id: string) => id === matches : matches
  const calls = CALL_LOCATIONS[request.protocol](request).filter(call => typeof call.id === 'string' && selected(call.id))
  if (calls.length !== 1)
    throw new Error(`The native request contains ${calls.length} tool calls for ${label}. Exactly one call is required.`)
  const call = calls[0]!
  if (typeof call.id !== 'string' || typeof call.name !== 'string' || call.name === '')
    throw new Error(`The native tool call for ${label} has no name.`)
  if ('value' in call)
    return { id: call.id, name: call.name, arguments: call.value }
  if (typeof call.encoded !== 'string')
    throw new Error(`The native tool call ${call.id} has no JSON argument text.`)
  try {
    return { id: call.id, name: call.name, arguments: JSON.parse(call.encoded) }
  }
  catch (error) {
    throw new Error(`The native tool call ${call.id} contains invalid JSON arguments.`, { cause: error })
  }
}

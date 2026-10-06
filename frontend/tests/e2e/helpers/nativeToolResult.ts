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

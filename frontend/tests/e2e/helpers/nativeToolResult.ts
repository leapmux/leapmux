import type { MockModelRequestRecord } from './mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'

function objectItems(value: unknown): Record<string, unknown>[] {
  return Array.isArray(value) ? value.filter(isObject) : []
}

/** Read only the native content for one tool call. Preserve its original type for provider validation. */
export function nativeToolResultContent(request: MockModelRequestRecord | undefined, callId: string): unknown {
  if (!request)
    throw new Error(`No native model request exists for tool call ${callId}.`)

  const body = isObject(request.body) ? request.body : null
  let results: unknown[]
  switch (request.protocol) {
    case 'openai-chat-completions':
      results = objectItems(body?.messages)
        .filter(message => message.role === 'tool' && message.tool_call_id === callId)
        .map(message => message.content)
      break
    case 'openai-responses':
      results = objectItems(body?.input)
        .filter(item => (item.type === 'function_call_output' || item.type === 'custom_tool_call_output') && item.call_id === callId)
        .map(item => item.output)
      break
    case 'google-generative-language':
      results = objectItems(body?.contents)
        .filter(message => message.role === 'user')
        .flatMap(message => objectItems(message.parts))
        .map(part => part.functionResponse)
        .filter(isObject)
        .filter(result => result.id === callId)
        .map(result => result.response)
      break
    case 'anthropic-messages':
      results = objectItems(body?.messages)
        .filter(message => message.role === 'user')
        .flatMap(message => objectItems(message.content))
        .filter(block => block.type === 'tool_result' && block.tool_use_id === callId)
        .map(block => block.content)
      break
    default:
      throw new Error(`Native tool results are unavailable for ${request.protocol}.`)
  }

  if (results.length !== 1)
    throw new Error(`The native request contains ${results.length} results for ${callId}. Exactly one result is required.`)
  const content = results[0]
  if (content === undefined || content === null)
    throw new Error(`The native result for ${callId} has no content.`)
  return content
}

/** Serialize one exact native result. The call arguments can repeat every offered answer. */
export function nativeToolResult(request: MockModelRequestRecord | undefined, callId: string): string {
  const content = nativeToolResultContent(request, callId)
  return typeof content === 'string' ? content : JSON.stringify(content)
}

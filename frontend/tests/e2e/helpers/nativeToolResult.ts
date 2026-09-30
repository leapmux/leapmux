import type { MockModelRequestRecord } from './mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'

function objectItems(value: unknown): Record<string, unknown>[] {
  return Array.isArray(value) ? value.filter(isObject) : []
}

/** Read only the native result for one tool call. The call arguments can repeat every offered answer. */
export function nativeToolResult(request: MockModelRequestRecord | undefined, callId: string): string {
  if (!request)
    throw new Error(`no native model request for tool call ${callId}`)

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
        .filter(item => item.type === 'function_call_output' && item.call_id === callId)
        .map(item => item.output)
      break
    case 'anthropic-messages':
      results = objectItems(body?.messages)
        .flatMap(message => objectItems(message.content))
        .filter(block => block.type === 'tool_result' && block.tool_use_id === callId)
        .map(block => block.content)
      break
    default:
      throw new Error(`native tool results are unavailable for ${request.protocol}`)
  }

  if (results.length !== 1)
    throw new Error(`expected one native result for ${callId}, received ${results.length}`)
  const content = results[0]
  if (content === undefined || content === null)
    throw new Error(`native result for ${callId} has no content`)
  return typeof content === 'string' ? content : JSON.stringify(content)
}

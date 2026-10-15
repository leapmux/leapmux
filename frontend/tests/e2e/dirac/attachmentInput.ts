import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeToolCallArguments } from '../helpers/nativeToolResult'

/** Select actual user feedback after Dirac ends its prior turn through the respond tool. */
export function diracAttachmentRequest(request: MockModelRequestRecord): MockModelRequestRecord {
  if (request.protocol !== 'openai-chat-completions' || !isObject(request.body) || !Array.isArray(request.body.messages))
    throw new Error('The Dirac attachment request requires its native message array.')
  const rows = request.body.messages
  const last: unknown = rows.at(-1)
  const previous: unknown = rows.at(-2)
  if (!isObject(last) || last.role !== 'user' || !isObject(previous) || previous.role !== 'tool')
    return request
  if (typeof previous.tool_call_id !== 'string')
    throw new Error('The preceding Dirac tool result has no call ID.')
  const call = nativeToolCallArguments(request, previous.tool_call_id)
  if (call.name !== 'respond' || !isObject(call.arguments) || call.arguments.operation !== 'complete')
    throw new Error('The Dirac attachment follows a tool result that did not complete the prior turn.')
  const first: unknown = Array.isArray(last.content) ? last.content[0] : undefined
  if (!isObject(first) || first.type !== 'text' || typeof first.text !== 'string'
    || !first.text.startsWith('<feedback>\n') || !first.text.endsWith('\n</feedback>')) {
    throw new Error('The Dirac attachment has no native user feedback envelope.')
  }
  // Keep the original row and its bytes. The generic reader otherwise treats a user row after a tool as tool output.
  return { ...request, body: { ...request.body, messages: [last] } }
}

import type { MockModelStep } from './mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'
import { CLAUDE_SUBAGENT_HANDBACK_TOOL, claudeSubagentHandbackToolCall } from './providerToolCalls'

function hasNativeHandback(requestBody: unknown): boolean {
  if (!isObject(requestBody) || !Array.isArray(requestBody.tools))
    return false
  return requestBody.tools.some((tool: unknown) => {
    if (!isObject(tool) || tool.name !== CLAUDE_SUBAGENT_HANDBACK_TOOL || !isObject(tool.input_schema))
      return false
    const schema = tool.input_schema
    return schema.type === 'object' && isObject(schema.properties) && isObject(schema.properties.message)
      && schema.properties.message.type === 'string' && Array.isArray(schema.required)
      && schema.required.length === 1 && schema.required[0] === 'message'
      && schema.additionalProperties === false
  })
}

function deliveredChildReport(requestBody: unknown): string | undefined {
  if (!hasNativeHandback(requestBody) || !isObject(requestBody) || !Array.isArray(requestBody.messages))
    return undefined
  const messages: unknown[] = requestBody.messages
  if (messages.some(message => !isObject(message) || typeof message.role !== 'string' || !['user', 'assistant', 'system'].includes(message.role)))
    return undefined
  const latestUser = messages.findLastIndex(message => isObject(message) && message.role === 'user')
  const user = messages[latestUser]
  if (!isObject(user) || !Array.isArray(user.content) || user.content.length !== 1)
    return undefined
  const result = user.content[0]
  if (!isObject(result) || result.type !== 'tool_result' || typeof result.tool_use_id !== 'string' || result.tool_use_id === ''
    || (Object.hasOwn(result, 'is_error') && result.is_error !== false)) {
    return undefined
  }
  const text = typeof result.content === 'string'
    ? result.content
    : Array.isArray(result.content) && result.content.length === 1 && isObject(result.content[0])
      && result.content[0].type === 'text' && typeof result.content[0].text === 'string'
      ? result.content[0].text
      : undefined
  if (text === undefined)
    return undefined
  let delivered: unknown
  try {
    delivered = JSON.parse(text)
  }
  catch {
    return undefined
  }
  if (!isObject(delivered) || delivered.success !== true || delivered.message !== 'Report delivered to your caller.')
    return undefined
  for (let index = latestUser - 1; index >= 0; index--) {
    const message = messages[index]
    if (!isObject(message))
      return undefined
    if (message.role === 'system')
      continue
    if (message.role !== 'assistant' || !Array.isArray(message.content))
      return undefined
    const calls = message.content.filter((block: unknown) => isObject(block) && block.type === 'tool_use' && block.id === result.tool_use_id)
    if (calls.length !== 1)
      return undefined
    const call = calls[0]
    if (!isObject(call) || call.name !== CLAUDE_SUBAGENT_HANDBACK_TOOL || !isObject(call.input)
      || typeof call.input.message !== 'string' || call.input.message.trim().length === 0) {
      return undefined
    }
    return call.input.message
  }
  return undefined
}

/** Finish only the native child continuation after its own report reached its caller. */
export function claudeDeliveredChildResponse(requestBody: unknown): MockModelStep | undefined {
  const report = deliveredChildReport(requestBody)
  return report === undefined ? undefined : { text: report }
}

/** Deliver a completed Claude child report through the tool that its actual request offers. */
export function applyClaudeChildHandback(requestBody: unknown, step: MockModelStep, responseId: string): MockModelStep {
  if (step.error || step.toolCalls?.length || step.text === undefined || step.text.trim().length === 0
    || !hasNativeHandback(requestBody) || deliveredChildReport(requestBody) !== undefined) {
    return step
  }
  return { ...step, toolCalls: [claudeSubagentHandbackToolCall(`${responseId}-handback`, step.text)] }
}

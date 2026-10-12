import { DEEPSEEK_HARNESS_CONTENT_TYPE, DEEPSEEK_HARNESS_EVENT, DEEPSEEK_HARNESS_FIELD, DEEPSEEK_HARNESS_SUPPLEMENT } from '~/generated/contracts/deepseek-harness-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/** Read the data of one durable native Session event. */
export function deepseekHarnessEventData(payload: unknown, type?: string): Record<string, unknown> | undefined {
  if (!isObject(payload) || typeof payload[DEEPSEEK_HARNESS_FIELD.Type] !== 'string' || (type !== undefined && payload[DEEPSEEK_HARNESS_FIELD.Type] !== type))
    return undefined
  return pickObject(payload, DEEPSEEK_HARNESS_FIELD.Data) ?? undefined
}

/** Select the exact native assistant block that the Worker persisted. */
export function deepseekHarnessAssistantBlock(payload: unknown): Record<string, unknown> | undefined {
  const data = deepseekHarnessEventData(payload, DEEPSEEK_HARNESS_EVENT.AssistantMessage)
  const message = pickObject(data, DEEPSEEK_HARNESS_FIELD.Message)
  const content = message?.[DEEPSEEK_HARNESS_FIELD.Content]
  if (!isObject(payload) || !Array.isArray(content))
    return undefined
  const index = payload[DEEPSEEK_HARNESS_SUPPLEMENT.BlockIndex]
  if (typeof index !== 'number' || !Number.isSafeInteger(index) || index < 0)
    return undefined
  const block = content[index]
  return isObject(block) ? block : undefined
}

/** Preserve the native text sequence. Image blocks take their own display path. */
export function deepseekHarnessContentText(content: unknown): string {
  if (!Array.isArray(content))
    return ''
  return content.flatMap(block => isObject(block) && block[DEEPSEEK_HARNESS_FIELD.Type] === DEEPSEEK_HARNESS_CONTENT_TYPE.Text
    && typeof block.text === 'string'
    ? [block.text]
    : []).join('')
}

/** Read the exact call identity from a call or result event. */
export function deepseekHarnessCallId(payload: unknown): string {
  const call = deepseekHarnessEventData(payload, DEEPSEEK_HARNESS_EVENT.ToolCall)
  if (call)
    return pickString(call, 'callId')
  const result = deepseekHarnessEventData(payload, DEEPSEEK_HARNESS_EVENT.ToolResult)
  return pickString(pickObject(result, DEEPSEEK_HARNESS_FIELD.Message), 'toolCallId')
}

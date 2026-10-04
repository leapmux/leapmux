import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeToolResultContent } from '../helpers/nativeToolResult'

export function deepseekHarnessToolResultText(request: MockModelRequestRecord | undefined, callId: string): string {
  if (request && request.protocol !== 'anthropic-messages')
    throw new Error('The DeepSeek Harness text result requires a native Anthropic Messages request.')
  const content = nativeToolResultContent(request, callId)
  const block: unknown = Array.isArray(content) && content.length === 1 ? content[0] : undefined
  if (!isObject(block) || block.type !== 'text' || typeof block.text !== 'string')
    throw new Error('The DeepSeek Harness result requires exactly one native text block.')
  return block.text
}

import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { DEEPSEEK_HARNESS_EVENT } from '~/generated/contracts/deepseek-harness-protocol'
import { retainedRowIsFinal } from '../registry'
import { deepseekHarnessCallId } from './protocol'

export function deepseekHarnessSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  if (!deepseekHarnessCallId(parsed.parentObject))
    return 'other'
  if (parsed.parentObject?.type === DEEPSEEK_HARNESS_EVENT.ToolResult)
    return 'result'
  if (parsed.parentObject?.type === DEEPSEEK_HARNESS_EVENT.ToolCall)
    return retainedRowIsFinal(parsed.completion) ? 'result' : 'request'
  return 'other'
}

export function deepseekHarnessRelatedMessages(parsed: ResolvedMessageContent) {
  const role = deepseekHarnessSpanRole(parsed)
  return role === 'request' ? ['result'] as const : role === 'result' ? ['request'] as const : []
}

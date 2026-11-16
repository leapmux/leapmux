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
  if (parsed.parentObject?.type === DEEPSEEK_HARNESS_EVENT.ToolCall) {
    return retainedRowIsFinal(parsed.completion)
      ? 'result'
      : 'request'
  }
  return 'other'
}

/**
 * A result event states neither the tool name nor the arguments: they are on the call
 * event. A call event draws from its own event only, also where its completion makes it
 * the final row of its span.
 */
export function deepseekHarnessNeedsRequest(parsed: ResolvedMessageContent): boolean {
  return deepseekHarnessCallId(parsed.parentObject) !== ''
    && parsed.parentObject?.type === DEEPSEEK_HARNESS_EVENT.ToolResult
}

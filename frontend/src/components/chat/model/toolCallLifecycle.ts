import type { ToolCallLifecycleFacts } from './toolCall'
import type { ToolCallStatus } from './toolCallStatus'
import { isFinishedToolCallStatus } from './toolCallStatus'

export const SYNTHETIC_TOOL_LIFECYCLE: ToolCallLifecycleFacts = {
  frameStatus: 'completed',
  providerOutcome: null,
  retainedOutcome: null,
  rowFinal: true,
  resultFrameLanded: true,
}

export function deriveToolCallStatus(
  facts: ToolCallLifecycleFacts,
  resultAvailable: boolean,
  statusOverride?: 'completed' | 'failed' | 'cancelled' | 'declined',
): ToolCallStatus {
  const completedStatus = (): ToolCallStatus => resultAvailable ? 'completed' : 'incomplete'
  if (statusOverride !== undefined)
    return statusOverride === 'completed' ? completedStatus() : statusOverride
  if (facts.providerOutcome === 'interrupted' || facts.retainedOutcome === 'interrupted')
    return 'cancelled'
  if (facts.providerOutcome === 'failed')
    return 'failed'
  if (facts.providerOutcome === 'declined')
    return 'declined'
  if (isFinishedToolCallStatus(facts.frameStatus))
    return facts.frameStatus === 'completed' ? completedStatus() : facts.frameStatus
  if (facts.retainedOutcome === 'failed')
    return 'failed'
  if (facts.resultFrameLanded || facts.rowFinal)
    return completedStatus()
  return facts.frameStatus
}

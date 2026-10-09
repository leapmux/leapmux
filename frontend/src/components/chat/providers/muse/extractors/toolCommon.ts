import type { ToolCallLifecycleFacts } from '../../../model/toolCall'
import type { ProviderToolOutcome } from '../../../model/toolOutcome'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { MUSE_ITEM_STATUS } from '~/generated/contracts/muse-protocol'
import { retainedOutcome, retainedRowIsFinal } from '../../registry'

const NATIVE_ITEM_OUTCOMES: ReadonlyMap<string, ProviderToolOutcome> = new Map([
  [MUSE_ITEM_STATUS.Completed, 'succeeded'],
  [MUSE_ITEM_STATUS.Failed, 'failed'],
  [MUSE_ITEM_STATUS.TimedOut, 'failed'],
  [MUSE_ITEM_STATUS.Rejected, 'declined'],
  [MUSE_ITEM_STATUS.Cancelled, 'interrupted'],
])

export interface MuseItemLifecycle {
  nativeFinal: boolean
  facts: ToolCallLifecycleFacts
}

/** Keep native finality, native outcome, and retained completion separate. */
export function museItemLifecycle(status: unknown, completion: MessageCompletion | undefined): MuseItemLifecycle {
  const nativeFinal = status !== MUSE_ITEM_STATUS.InProgress
  const providerOutcome = typeof status === 'string' ? NATIVE_ITEM_OUTCOMES.get(status) ?? null : null
  const resultFrameLanded = providerOutcome !== null
  return {
    nativeFinal,
    facts: {
      frameStatus: resultFrameLanded ? 'completed' : nativeFinal ? 'incomplete' : 'in_progress',
      providerOutcome,
      retainedOutcome: retainedOutcome(completion),
      rowFinal: nativeFinal || retainedRowIsFinal(completion),
      resultFrameLanded,
    },
  }
}

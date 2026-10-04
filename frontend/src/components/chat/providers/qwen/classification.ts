import { QWEN_METHOD } from '~/generated/contracts/qwen-protocol'
import { pickObject } from '~/lib/jsonPick'

/**
 * The stop reason of a turn that Qwen started by itself, or undefined for any other
 * frame.
 *
 * Qwen runs a turn of its own for a goal, for a background task that finished, or
 * for a scheduled job, and it ends such a turn with `_qwencode/end_turn`. The worker
 * stores that notification as the end of the turn, as it stores the prompt response
 * of a turn LeapMux started. A notification that states no reason still ends the
 * turn, so it answers the empty reason, which the divider reads as a plain end.
 */
export function qwenAgentTurnEnd(parent: Record<string, unknown>): string | undefined {
  if (parent.method !== QWEN_METHOD.EndTurn)
    return undefined
  const reason = pickObject(parent, 'params')?.reason
  return typeof reason === 'string' ? reason : ''
}

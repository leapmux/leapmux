import { QWEN_METHOD } from '~/generated/contracts/qwen-protocol'
import { pickObject } from '~/lib/jsonPick'

/**
 * The stop reason of a turn that Qwen started by itself, or undefined for any other
 * frame.
 *
 * Qwen starts its own turns for goals and completed background tasks.
 * Its `_qwencode/end_turn` notification states the end.
 * The worker stores that native frame, as it stores a prompt response.
 * An absent reason still ends the turn. The empty reason gives the divider no qualifier.
 * The launcher disables scheduled prompts because Qwen emits no start or end frame for them.
 */
export function qwenAgentTurnEnd(parent: Record<string, unknown>): string | undefined {
  if (parent.method !== QWEN_METHOD.EndTurn)
    return undefined
  const reason = pickObject(parent, 'params')?.reason
  return typeof reason === 'string' ? reason : ''
}

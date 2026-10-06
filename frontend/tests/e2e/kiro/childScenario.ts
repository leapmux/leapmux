import type { MockModelMatcher, MockModelPattern } from '../helpers/mockModelScript'
import { KIRO_CHILD_AGENT } from '../helpers/providerToolCalls'
import { HELD_CHILD_TASK } from '../helpers/subagentRegistry'

/**
 * Match the own turn of a Kiro child whose last user text matches `user`.
 * The request of a Kiro child states the agent mode of the context gatherer, and the matcher requires that mode.
 */
export function kiroChildTurn(user: MockModelPattern): MockModelMatcher {
  return { body: `"agentMode":"${KIRO_CHILD_AGENT}"`, user }
}

/**
 * The turn of the held child of `openHeldChildTab`, which the interrupt and send cells hold.
 * The spawn call of the held child starts the context gatherer (`spawnSubagentToolCall`), so the agent mode selects the
 * child, and a parent turn that quotes the task does not match.
 */
export const KIRO_HELD_CHILD_TURN: MockModelMatcher = kiroChildTurn(HELD_CHILD_TASK)

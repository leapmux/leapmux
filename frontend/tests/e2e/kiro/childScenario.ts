import type { MockModelMatcher, MockModelPattern } from '../helpers/mockModelScript'
import { HELD_CHILD_TASK } from '../helpers/subagentRegistry'

/** The name of the context gatherer, the subagent that Kiro starts for a spawn, which its row and report state. */
export const KIRO_CHILD_AGENT = 'context-gatherer'

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

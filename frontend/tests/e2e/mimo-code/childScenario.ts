import type { MockModelMatcher } from '../helpers/mockModelScript'
import { childTaskAtStart } from '../helpers/runningChildProof'
import { HELD_CHILD_TASK } from '../helpers/subagentRegistry'

/**
 * Match the own turn of a MiMo Code child from its task.
 * The turn of a MiMo child opens with its task. The requests of the parent carry the spawn call, which quotes the
 * task, so the matcher anchors the task at the start of the last user text, and those requests do not match.
 */
export function mimoChildTurn(task: string): MockModelMatcher {
  return childTaskAtStart(task)
}

/** The turn of the held child of `openHeldChildTab`, which the interrupt cell holds. */
export const MIMO_HELD_CHILD_TURN: MockModelMatcher = mimoChildTurn(HELD_CHILD_TASK)

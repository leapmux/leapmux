import type { MockModelMatcher } from '../helpers/mockModelScript'
import { childTaskAtStart } from '../helpers/runningChildProof'
import { HELD_CHILD_TASK } from '../helpers/subagentRegistry'

/**
 * Match the own turn of a Qwen Code child from its task.
 *
 * Qwen Code sends the `prompt` of the spawn call, unchanged, as a user message of its own. A workflow `agent()` call
 * does the same. The startup reminders go into an earlier user message, and each tool result is a `tool` message, so
 * the task starts the last user text of each child turn. The matcher anchors the task there. A turn that only quotes
 * the task, such as the title request of the child, does not match.
 */
export function qwenChildTurn(task: string): MockModelMatcher {
  return childTaskAtStart(task)
}

/** The turn of the held child of `openHeldChildTab`, which the interrupt and send cells hold. */
export const QWEN_HELD_CHILD_TURN: MockModelMatcher = qwenChildTurn(HELD_CHILD_TASK)

import type { MockModelMatcher, MockModelPattern } from '../helpers/mockModelScript'
import type { HeldChildCase } from '../helpers/subagentRegistry'
import { ohMyPiYieldToolCall } from '../helpers/providerToolCalls'
import { HELD_CHILD_NAME, HELD_CHILD_REPORT, HELD_CHILD_TASK } from '../helpers/subagentRegistry'

/** The line with which Oh My Pi starts the conversation of a child, before the task text. */
export const OMP_ASSIGNMENT_OPENING = 'Complete assignment thoroughly'

/**
 * Match the own turn of an Oh My Pi child whose last user text matches `user`.
 *
 * A `<system-reminder>` block precedes each prompt, so the text never starts with the task. The `yield` tool is the
 * second condition: Oh My Pi offers it only to a child, so a parent request never matches the rule.
 */
export function ohMyPiChildTurn(user: MockModelPattern = OMP_ASSIGNMENT_OPENING): MockModelMatcher {
  return { user, body: '"name":"yield"' }
}

/**
 * The held child of `openHeldChildTab` for the interrupt and send cells, as a new object for each call.
 * Oh My Pi ends a child run through its `yield` tool, so the held answer is a `yield` call, and the registry row shows
 * the name of the child. The child turn goes through {@link ohMyPiChildTurn}, so a parent turn that quotes the task
 * does not match.
 */
export function ohMyPiHeldChild(): HeldChildCase {
  return {
    rowTitle: HELD_CHILD_NAME,
    heldAnswer: { toolCalls: [ohMyPiYieldToolCall('held-child-yield', HELD_CHILD_REPORT)] },
    childTurn: ohMyPiChildTurn(HELD_CHILD_TASK),
    rootTurnsAfterSpawn: [{ text: 'The actual native child completed.' }],
  }
}

import type { MockModelMatcher, MockModelPattern } from '../helpers/mockModelScript'

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

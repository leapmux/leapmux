import type { MockModelMatcher, MockModelPattern } from '../helpers/mockModelScript'

/** The words that the system prompt of a Qoder CLI child states. The system prompt of a root agent does not. */
export const QODER_CHILD_SYSTEM = 'You are an agent for Qoder'

/**
 * Match the own turn of a Qoder CLI child whose last user text matches `user`.
 * The matcher also requires the system prompt of a child, so a root turn that quotes the task does not match.
 */
export function qoderChildTurn(user: MockModelPattern): MockModelMatcher {
  return { system: QODER_CHILD_SYSTEM, user }
}

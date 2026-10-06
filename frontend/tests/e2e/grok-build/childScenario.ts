import type { MockModelMatcher, MockModelPattern } from '../helpers/mockModelScript'

/**
 * The words that open the system prompt of the own turn of a Grok Build subagent.
 *
 * Only the own turn of the child states these words, so a rule that requires them takes no other request that carries
 * the same user text. The session title of the child carries the child prompt too, but the housekeeping title rule
 * has high priority and answers it first.
 */
export const GROK_SUBAGENT_SYSTEM = 'You are a Grok Build subagent\\b'

/** Match the own turn of a Grok Build child whose last user text matches `user`. */
export function grokChildTurn(user: MockModelPattern): MockModelMatcher {
  return { system: GROK_SUBAGENT_SYSTEM, user }
}

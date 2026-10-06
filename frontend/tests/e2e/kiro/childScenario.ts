import type { MockModelMatcher, MockModelPattern } from '../helpers/mockModelScript'

/** The name of the context gatherer, the subagent that Kiro starts for a spawn, which its row and report state. */
export const KIRO_CHILD_AGENT = 'context-gatherer'

/**
 * Match the own turn of a Kiro child whose last user text matches `user`.
 * The request of a Kiro child states the agent mode of the context gatherer, and the matcher requires that mode.
 */
export function kiroChildTurn(user: MockModelPattern): MockModelMatcher {
  return { body: `"agentMode":"${KIRO_CHILD_AGENT}"`, user }
}

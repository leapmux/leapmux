import type { MockModelRule } from '../helpers/mockModelScript'

/** The policy prefix of Codewhale's native automatic reviewer. */
export const CODEWHALE_REVIEWER_POLICY = '^You are the Auto-Review guardian for a coding agent'

/** Keep Codewhale's native verdict shape in its provider test module. */
export function codewhaleAutoReviewRule(name: string, decision: 'allow' | 'deny', reason: string): MockModelRule {
  if (!name.trim() || !reason.trim())
    throw new Error('A Codewhale reviewer rule requires a name and reason.')
  return {
    name,
    when: { system: CODEWHALE_REVIEWER_POLICY },
    respond: { text: JSON.stringify({ risk_level: decision === 'allow' ? 'low' : 'high', decision, reason }) },
  }
}

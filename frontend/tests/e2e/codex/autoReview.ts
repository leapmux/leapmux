import type { MockModelRequestRecord, MockModelRule } from '../helpers/mockModelScript'
import { systemText } from '../helpers/mockModelScript'

/**
 * The sentence that opens the policy of Codex's automatic reviewer
 * (`prompts/templates/guardian/policy_template.md` in Codex). Codex sends the policy as a
 * developer row of the reviewer's own request, and no turn of the agent holds it. So a
 * rule that matches it answers the reviewer and never a turn of the agent.
 *
 * The reviewer request quotes the agent's transcript, and the transcript holds the
 * marked prompt. So the request reaches the script of the test that sent the prompt.
 */
export const CODEX_REVIEWER_POLICY = 'You are judging one planned coding-agent action'

/**
 * The developer text with which Codex states a reader's approval of a denied action to
 * the model (`MANUAL_APPROVAL_DEVELOPER_PREFIX` in Codex's `guardian-context`).
 */
export const CODEX_MANUAL_APPROVAL_PREFIX = 'The user has manually approved a specific action that was previously `Rejected`.'

/** The verdict that Codex's reviewer returns, in the JSON shape that its policy requires. */
export interface CodexReviewVerdict {
  outcome: 'allow' | 'deny'
  risk_level: 'low' | 'medium' | 'high' | 'critical'
  user_authorization: 'unknown' | 'low' | 'medium' | 'high'
  rationale: string
}

/** A rule that answers every request of Codex's reviewer with one verdict. */
export function codexAutoReviewRule(name: string, verdict: CodexReviewVerdict): MockModelRule {
  if (name.trim() === '')
    throw new Error('A Codex auto-review rule needs a name.')
  if (verdict.rationale.trim() === '')
    throw new Error('A Codex auto-review verdict needs a rationale.')
  return {
    name,
    when: { system: CODEX_REVIEWER_POLICY },
    respond: {
      text: JSON.stringify(verdict),
    },
  }
}

/**
 * The approval of a denied action that one model request states to the model, or
 * undefined when the request states none. The text runs from Codex's approval prefix to
 * the end of the system text, so it holds the approved action and its outcome.
 */
export function codexManualApproval(request: Pick<MockModelRequestRecord, 'protocol' | 'body'>): string | undefined {
  const text = systemText(request.protocol, request.body)
  const start = text.indexOf(CODEX_MANUAL_APPROVAL_PREFIX)
  return start < 0
    ? undefined
    : text.slice(start)
}

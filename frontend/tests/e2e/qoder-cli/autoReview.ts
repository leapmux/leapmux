import type { MockModelRule } from '../helpers/mockModelScript'

/** The policy prefix of Qoder's native LLM classifier in qodercli 1.1.71. */
export const QODER_REVIEWER_POLICY = 'You are a security classifier for auto permission mode.'

/** Answer Qoder's classifier without consuming an ordinary conversation step. */
export function qoderAutoReviewRule(name: string, shouldBlock: boolean, reason: string): MockModelRule {
  if (!name.trim() || !reason.trim())
    throw new Error('A Qoder reviewer rule requires a name and reason.')
  return {
    name,
    when: { system: QODER_REVIEWER_POLICY },
    respond: { text: JSON.stringify({ shouldBlock, reason }) },
  }
}

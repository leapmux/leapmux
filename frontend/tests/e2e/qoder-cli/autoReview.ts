import type { MockModelRule } from '../helpers/mockModelScript'
import { escapeRegExp } from '../../../src/lib/regexp'

/** The policy prefix of Qoder's native LLM classifier in qodercli 1.1.86. */
export const QODER_REVIEWER_POLICY = 'You are a security monitor for an autonomous AI coding agent.'

/** Answer Qoder's classifier without consuming an ordinary conversation step. */
export function qoderAutoReviewRule(name: string, shouldBlock: boolean, reason: string): MockModelRule {
  if (!name.trim() || !reason.trim())
    throw new Error('A Qoder reviewer rule requires a name and reason.')
  return {
    name,
    when: { system: `^${escapeRegExp(QODER_REVIEWER_POLICY)}(?:\n|$)` },
    respond: {
      text: JSON.stringify({ shouldBlock, reason }),
    },
  }
}

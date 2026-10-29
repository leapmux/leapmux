import type { MockModelRule } from '../helpers/mockModelScript'
import { escapeRegExp } from '../../../src/lib/regexp'

/**
 * The first line of the prompt of ZCode's own completion verifier (ZCode 3.14.5).
 * The verifier runs after every goal turn and reads a JSON verdict from the reply.
 */
export const ZCODE_VERIFIER_PROMPT = 'Verify whether the active session goal is actually complete.'

/** The verdict of a check that finds the goal incomplete: the goal loop continues. */
export const NOT_PASSED_VERDICT = JSON.stringify({ passed: false, reason: 'The tests still fail.', nextAction: 'Fix the failing test.' })

/**
 * The verdict of a check that keeps the goal active and stops the goal loop.
 *
 * ZCode continues its goal loop only for a failed check that states a next action. A failed check with no next action
 * ("Goal continuation skipped after verifier failed without next action") ends the loop and leaves the goal active, so
 * the scenario can pause, resume and clear it with no prompt running.
 */
export const NOT_PASSED_NO_NEXT_ACTION_VERDICT = JSON.stringify({ passed: false, reason: 'The user keeps working toward this goal.' })

/** The verdict of a check that finds the goal complete: the task ends. */
export const PASSED_VERDICT = JSON.stringify({ passed: true, reason: 'All tests pass.' })

/**
 * Build the rule that answers one verifier call with a JSON verdict.
 *
 * A prose reply is not a verdict. ZCode reads "The completion verifier did not return valid JSON." as passed and
 * completes the goal. Every scenario that keeps a goal active therefore scripts a JSON verdict.
 */
export function zcodeVerifierRule(name: string, verdict: string, options: { once?: boolean } = {}): MockModelRule {
  return {
    name,
    priority: 'high',
    when: {
      body: escapeRegExp(ZCODE_VERIFIER_PROMPT),
    },
    respond: { text: verdict },
    ...(options.once === true
      ? { once: true }
      : {}),
  }
}

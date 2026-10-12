import type { MockModelRule } from '../helpers/mockModelScript'

/**
 * The classifier call that Junie makes for a prompt that follows a plan that is still pending.
 *
 * A revised plan stays pending. Junie asks its model whether the next prompt approves the plan (`YES`),
 * abandons it (`DROP`), or changes it (`NO`). The answer `NO` keeps Junie in Plan mode, and Junie plans again.
 * This request has no ordered step, so a rule answers it. The rule matches the system prompt of that request
 * and no other text.
 */
export const JUNIE_PLAN_REPLY_RULE: MockModelRule = {
  name: 'junie-plan-reply',
  when: { system: 'classifying a user\'s reply after they just saw a proposed plan' },
  respond: { text: 'NO' },
}

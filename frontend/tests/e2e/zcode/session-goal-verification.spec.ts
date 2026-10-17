import { expect } from '@playwright/test'
import { expectGoalStatus, scriptedObjective, setGoal } from '../helpers/goalsAndTodos'
import { expectNoUnrenderedRow } from '../helpers/nativeConversation'
import { messageContents, visibleOnly } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'
import { NOT_PASSED_VERDICT, PASSED_VERDICT, zcodeVerifierRule } from './goalVerifier'

zcodeTest('session-goal-verification: renders both goal check verdicts and ends the achieved goal', async ({ native }) => {
  const { page, modelScript } = native
  // ZCode runs its goal turns with the objective as the prompt. The fallback
  // answers each one. The two verifier calls take the two rules in order.
  await modelScript.fallback({ text: 'Goal turn complete.' })
  // One call carries both rules. Each `rule` call PREPENDS its rules to the
  // scenario, so two calls would answer the first check with the second rule and
  // end the goal after one iteration. Inside one call the declaration order
  // holds, so the not-passed verdict answers the first check.
  await modelScript.rule(
    zcodeVerifierRule('zcode-goal-verifier-not-passed', NOT_PASSED_VERDICT, { once: true }),
    zcodeVerifierRule('zcode-goal-verifier-passed', PASSED_VERDICT, { once: true }),
  )

  await setGoal(page, scriptedObjective(modelScript, 'Run the goal loop until its verification passes.'))

  // Each check draws one row with its verdict. The row is the goal check's own
  // frame, so the wording states the iteration, the verdict and its reason.
  const rows = visibleOnly(messageContents(page))
  await expect(rows.filter({ hasText: 'Iteration 1 · Goal incomplete, continuing — The tests still fail. Next: Fix the failing test.' }).first()).toBeVisible()
  await expect(rows.filter({ hasText: 'Iteration 2 · Goal complete, ending task — All tests pass.' }).first()).toBeVisible()
  await expectGoalStatus(page, 'done')

  // The side queries stay off the transcript: the goal summary title and the
  // verifier's raw JSON verdict are the app-server's own model calls.
  await expect(rows.filter({ hasText: '{"title":' })).toHaveCount(0)
  await expect(rows.filter({ hasText: '{"passed":' })).toHaveCount(0)
  await expectNoUnrenderedRow(page)
})

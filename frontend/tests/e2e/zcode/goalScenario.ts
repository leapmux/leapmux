import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { pauseResumeClearGoal, scriptedObjective, setGoal } from '../helpers/goalsAndTodos'
import { messageContents, visibleOnly } from '../helpers/ui'
import { NOT_PASSED_NO_NEXT_ACTION_VERDICT, zcodeVerifierRule } from './goalVerifier'

/** Set, pause, resume, and clear a native ZCode session goal. The objective and the paused status survive a reload. */
export async function exerciseZCodeGoalCycle(context: NativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  // ZCode runs native goal turns with the objective as the prompt, and this scenario does not count them.
  // The fallback answers each one.
  await modelScript.fallback({ text: 'Goal turn complete.' })
  // The completion verifier must not reach that fallback: a prose reply is no JSON verdict, and ZCode reads an
  // unparseable verdict as passed, which completes the goal before the scenario can pause it. The rule reports a
  // failed check with no next action instead. ZCode then keeps the goal active and stops its goal loop, so the pause,
  // resume and clear below reach the session with no goal prompt running.
  await modelScript.rule(zcodeVerifierRule('zcode-goal-verifier-stops-goal-loop', NOT_PASSED_NO_NEXT_ACTION_VERDICT))
  const objective = scriptedObjective(modelScript, 'Keep the ZCode session goal until the browser clears it.')
  await setGoal(page, objective)
  // The check states no next action, so its row states that the goal stays active, not that it continues. The row is
  // also the point at which the goal loop stopped, so the pause below reaches a session with no goal prompt running.
  const rows = visibleOnly(messageContents(page))
  await expect(rows.filter({ hasText: 'Iteration 1 · Goal incomplete; the goal stays active — The user keeps working toward this goal.' }).first()).toBeVisible()
  await expect(rows.filter({ hasText: 'Goal incomplete, continuing' })).toHaveCount(0)
  await pauseResumeClearGoal(page, objective)
}

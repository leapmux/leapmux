import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expandGoalsAndTodosSection, expectEmptyGoalCard, pauseResumeClearGoal, setGoal } from '../helpers/goalsAndTodos'
import { expect } from '../qoder-fixtures'

/** Exercise the actual native control and retain every original assertion. */
export async function exerciseNativeGoalCycle(context: NativeScenarioContext): Promise<void> {
  const { page, modelScript } = context

  await expandGoalsAndTodosSection(page)
  await expectEmptyGoalCard(page)
  const objective = 'Keep the Qoder goal until I clear it.'
  await setGoal(page, objective)
  await pauseResumeClearGoal(page, objective)
  expect((await modelScript.status()).requests).toHaveLength(0)
}

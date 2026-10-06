import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { expandGoalsAndTodosSection, expectEmptyGoalCard, pauseResumeClearGoal, setGoal } from '../helpers/goalsAndTodos'

/**
 * Set, pause, resume, and clear a native Qoder session goal through the goal card.
 * Qoder changes its goal without a model turn, so the scenario requires that no model request reached the script.
 */
export async function exerciseNativeGoalCycle(context: NativeScenarioContext): Promise<void> {
  const { page, modelScript } = context

  await expandGoalsAndTodosSection(page)
  await expectEmptyGoalCard(page)
  const objective = 'Keep the Qoder goal until I clear it.'
  await setGoal(page, objective)
  await pauseResumeClearGoal(page, objective)
  expect((await modelScript.status()).requests).toHaveLength(0)
}

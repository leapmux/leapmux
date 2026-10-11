import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, goalAction, openGoalMenu, setGoal } from '../helpers/goalsAndTodos'
import { inputQueue, queuePauseButton } from '../helpers/ui'

/**
 * Set, pause, resume, and clear a native Copilot session goal while the input queue is paused.
 * Copilot answers a set with a continuation prompt of its own. The paused queue holds that prompt,
 * so the scenario needs no model turn, and the held prompt proves that the runtime acted on the goal.
 */
export async function exerciseCopilotGoalCycle(page: Page): Promise<void> {
  const objective = 'Keep the native Copilot objective until the browser clears it.'
  const queue = inputQueue(page)

  await expandGoalsAndTodosSection(page)
  await queuePauseButton(page).click()
  await setGoal(page, objective)
  // The runtime's own continuation prompt waits in the queue. LeapMux never writes
  // one of its own, so an empty queue means that the effect was lost.
  await expect(queue).toBeVisible()

  await openGoalMenu(page)
  await goalAction(page, 'pause').click()
  await expectGoalStatus(page, 'paused')
  await expectGoalObjective(page, objective)

  // The objective survives a reload, because the runtime stores it.
  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expectGoalObjective(page, objective)
  await expectGoalStatus(page, 'paused')

  await openGoalMenu(page)
  await goalAction(page, 'resume').click()
  await expectGoalStatus(page, 'active')
  await expect(queue).toBeVisible()

  await clearGoal(page)
  await expectEmptyGoalCard(page)
}

import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, goalAction, openGoalMenu, submitGoal } from '../helpers/goalsAndTodos'
import { queuePauseButton } from '../helpers/ui'

copilotTest('sets, pauses, resumes and clears a native session goal', async ({ authenticatedCopilotWorkspace, page }) => {
  void authenticatedCopilotWorkspace
  const objective = 'Keep the native Copilot objective until the browser clears it.'
  const queue = page.locator('[data-testid="agent-input-queue"]:visible')
  const pauseButton = queuePauseButton(page)

  await expandGoalsAndTodosSection(page)
  await pauseButton.click()
  await submitGoal(page, objective)

  await expectGoalObjective(page, objective)
  await expectGoalStatus(page, 'active')
  // The runtime's own continuation prompt waits in the queue. LeapMux never writes
  // one of its own, so a queue with nothing in it would mean the effect was lost.
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
})

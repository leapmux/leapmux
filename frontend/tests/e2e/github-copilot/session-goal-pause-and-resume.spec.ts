import { expect } from '@playwright/test'
import { COPILOT_E2E_SKIP_REASON, copilotTest } from '../copilot-fixtures'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from '../helpers/subagentRegistry'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

copilotTest('session-goal-pause-and-resume: sets, pauses, resumes and clears a native session goal', async ({ authenticatedCopilotWorkspace, page }) => {
  void authenticatedCopilotWorkspace
  const objective = 'Keep the native Copilot objective until the browser clears it.'
  const queue = page.locator('[data-testid="agent-input-queue"]:visible')
  const pauseButton = page.locator('[data-testid="queue-pause-button"]:visible')

  await expandGoalsAndTodosSection(page)
  await pauseButton.click()
  await goalAction(page, 'set').click()
  await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(objective)
  await page.locator('[data-testid="set-goal-submit"]:visible').click()

  await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(objective)
  await expectGoalStatus(page, 'active')
  // The runtime's own continuation prompt waits in the queue. LeapMux never writes
  // one of its own, so a queue with nothing in it would mean the effect was lost.
  await expect(queue).toBeVisible()

  await openGoalMenu(page)
  await goalAction(page, 'pause').click()
  await expectGoalStatus(page, 'paused')
  await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(objective)

  // The objective survives a reload, because the runtime stores it.
  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText(objective)
  await expectGoalStatus(page, 'paused')

  await openGoalMenu(page)
  await goalAction(page, 'resume').click()
  await expectGoalStatus(page, 'active')
  await expect(queue).toBeVisible()

  await openGoalMenu(page)
  await goalAction(page, 'clear').click()
  await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
})

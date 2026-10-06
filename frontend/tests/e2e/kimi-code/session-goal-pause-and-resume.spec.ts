import { expect } from '@playwright/test'
import { finishCleanup } from '../helpers/cleanup'
import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalStatus, goalAction, goalCard, openGoalMenu, submitGoal } from '../helpers/goalsAndTodos'
import { waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('Kimi Code session goal', () => {
  kimiTest('set a goal from the panel, pause it, resume it, and clear it', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    const first = 'kimi-goal-first-turn'
    const second = 'kimi-goal-resumed-turn'
    await modelScript.fallback({ text: 'Working on the objective.', gate: first })
    try {
      await expandGoalsAndTodosSection(page)
      await expectEmptyGoalCard(page)
      await submitGoal(page, modelScript.prompt('Keep inspecting this repository until I pause the goal.'))
      await expect(goalCard(page)).toBeVisible()
      await expect.poll(() => page.locator('[data-testid="goal-objective"]:visible').textContent()).toContain('Keep inspecting this repository')
      await expectGoalStatus(page, 'active')
      await modelScript.waitForGate(first)
      expect((await modelScript.status()).requests.length).toBeGreaterThan(0)
      await openGoalMenu(page)
      await goalAction(page, 'pause').click()
      await expectGoalStatus(page, 'paused')
      await page.reload()
      await expandGoalsAndTodosSection(page)
      await expectGoalStatus(page, 'paused')
      await modelScript.fallback({ text: 'The actual resumed goal turn reached the model.', gate: second })
      await openGoalMenu(page)
      await goalAction(page, 'resume').click()
      await expectGoalStatus(page, 'active')
      // The pause aborted the held first turn: a paused goal must not keep
      // working, so its gate holds no waiter any more. The resume starts a new
      // turn, which the replacement fallback holds at its own gate.
      await modelScript.waitForGate(second)
      expect((await modelScript.status()).requests.length).toBeGreaterThan(1)
      // Clear the goal while the resumed turn is still held. A released gate stays open, and the
      // goal loop of Kimi then asks the model again at once. That loop answered 200 requests, the
      // limit of the fallback, before the click that clears the goal arrived.
      await clearGoal(page)
      await expectEmptyGoalCard(page)
      // Kimi may abort the held turn when the goal clears, as it does when the goal pauses.
      await modelScript.releaseGateIfHeld(second)
      await waitForAgentIdle(page)
    }
    finally {
      await finishCleanup([modelScript.releaseGateIfHeld(first), modelScript.releaseGateIfHeld(second)])
    }
  })
})

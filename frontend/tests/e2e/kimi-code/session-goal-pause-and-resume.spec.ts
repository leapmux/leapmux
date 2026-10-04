import { expect } from '@playwright/test'
import { finishCleanup } from '../helpers/cleanup'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, goalCard, openGoalMenu } from '../helpers/subagentRegistry'
import { waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

kimiTest.describe('Kimi Code session goal', () => {
  kimiTest('set a goal from the panel, pause it, resume it, and clear it', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    const first = 'kimi-goal-first-turn'
    const second = 'kimi-goal-resumed-turn'
    await modelScript.fallback({ text: 'Working on the objective.', gate: first })
    try {
      await expandGoalsAndTodosSection(page)
      await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
      await goalAction(page, 'set').click()
      await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(modelScript.prompt('Keep inspecting this repository until I pause the goal.'))
      await page.locator('[data-testid="set-goal-submit"]:visible').click()
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
      await modelScript.releaseGate(first)
      await modelScript.waitForGate(second)
      expect((await modelScript.status()).requests.length).toBeGreaterThan(1)
      await openGoalMenu(page)
      await goalAction(page, 'clear').click()
      await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
      await modelScript.releaseGateIfHeld(second)
      await waitForAgentIdle(page)
    }
    finally {
      await finishCleanup([modelScript.releaseGateIfHeld(first), modelScript.releaseGateIfHeld(second)])
    }
  })
})

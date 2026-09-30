import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from './codebuddy-fixtures'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from './helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle, waitForSettingsHydrated } from './helpers/ui'

codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

codebuddyTest.describe('CodeBuddy Code session goal', () => {
  codebuddyTest('sets and clears a goal through native commands', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await waitForSettingsHydrated(page, 'permissionMode')
    await modelScript.rule({
      name: 'native goal command turn',
      when: { user: '<user_query>/goal' },
      respond: { text: 'The goal command was processed.' },
    })
    await modelScript.queue({ text: 'Goal controls are ready.' })
    await sendMessage(page, modelScript.prompt('Reply once before the goal command.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expandGoalsAndTodosSection(page)
    await goalAction(page, 'set').click()
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill('Keep the example objective active.')
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expectGoalStatus(page, 'active')
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText('Keep the example objective active.')

    await openGoalMenu(page)
    await expect(goalAction(page, 'pause')).toHaveCount(0)
    await expect(goalAction(page, 'resume')).toHaveCount(0)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
    await waitForAgentIdle(page)
    expect((await modelScript.status()).ruleMatches['native goal command turn']).toBeGreaterThan(1)

    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  })
})

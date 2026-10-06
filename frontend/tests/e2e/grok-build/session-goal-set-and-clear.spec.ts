import { expect } from '@playwright/test'
import { grokTest, openGrokAgent } from '../grok-fixtures'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from '../helpers/subagentRegistry'
import { openWorkspace, waitForSettingsHydrated } from '../helpers/ui'

grokTest.describe('Grok Build session goal', () => {
  grokTest('sets, follows and clears a native goal', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { approvalMode: 'always-approve' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    // Every call Grok makes for the goal quotes the objective, which carries
    // the marker, and none of them is a turn the test sends. The number of such
    // calls is Grok's own, so a rule answers them all.
    await modelScript.rule({
      name: 'every goal call answers with prose',
      when: { body: 'Keep the probe objective' },
      respond: { text: 'No plan.' },
    })

    await expandGoalsAndTodosSection(page)
    await goalAction(page, 'set').click()
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(modelScript.prompt('Keep the probe objective.'))
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText('Keep the probe objective.')

    await expectGoalStatus(page, 'paused')

    const callsBeforeResume = (await modelScript.status()).ruleMatches['every goal call answers with prose'] ?? 0
    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await expect.poll(async () => (await modelScript.status()).ruleMatches['every goal call answers with prose'] ?? 0).toBeGreaterThan(callsBeforeResume)
    await expectGoalStatus(page, 'paused')

    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  })
})

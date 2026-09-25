import { expect, GROK_E2E_SKIP_REASON, grokTest, openGrokAgent } from './grok-fixtures'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from './helpers/subagentRegistry'
import { openWorkspace, waitForSettingsHydrated } from './helpers/ui'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

/**
 * 148 -- Grok Build session goal.
 *
 * The goal card drives Grok's own `/goal` command, and Grok reports the goal
 * through its session notifications. Grok opens a goal with a plan-writer
 * subagent; this test answers every model call of the goal with prose, so the
 * writer produces no plan and Grok pauses the goal and says so. That pause is a
 * state the card must show, and a clear must leave the card empty.
 */
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

    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  })
})

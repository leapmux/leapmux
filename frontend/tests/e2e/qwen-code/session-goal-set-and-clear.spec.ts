import { expect } from '@playwright/test'
import { clearGoal, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, goalAction, openGoalMenu, submitGoal } from '../helpers/goalsAndTodos'
import { openWorkspace, waitForSettingsHydrated } from '../helpers/ui'
import { openQwenAgent, qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code settings and goal', () => {
  // The goal card changes the goal through Qwen's goal control. Qwen starts the goal turns.
  // Each turn carries the objective marker and reaches this script. A turn without progress counts toward the automatic pause.
  qwenTest('sets, follows and clears a native goal', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await modelScript.rule({
      name: 'every goal turn answers DONE',
      when: { body: 'Reply with the word DONE' },
      respond: { text: 'DONE' },
    })

    await submitGoal(page, modelScript.prompt('Reply with the word DONE.'))
    await expectGoalObjective(page, 'Reply with the word DONE.')

    // Qwen pauses a goal after turns that record no progress, and it states why.
    await expectGoalStatus(page, 'paused')
    // A turn Qwen started by itself ends with its own notification, which draws
    // the same divider as a turn the reader started. The goal control starts no
    // turn of the reader's, so each divider is the end of a goal round, and a
    // second one proves that a later round drew its own.
    await expect(page.locator('[data-testid="result-divider"]:visible').nth(1)).toBeVisible()

    const roundsBeforeResume = (await modelScript.status()).ruleMatches['every goal turn answers DONE'] ?? 0
    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await expect.poll(async () => (await modelScript.status()).ruleMatches['every goal turn answers DONE'] ?? 0).toBeGreaterThan(roundsBeforeResume)
    await expectGoalStatus(page, 'paused')

    await clearGoal(page)
    await expectEmptyGoalCard(page)
  })
})

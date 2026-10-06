import { expect } from '@playwright/test'
import { GROK_AGENT, grokTest } from '../grok-fixtures'
import { clearGoal, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, goalAction, openGoalMenu, scriptedObjective, submitGoal } from '../helpers/goalsAndTodos'
import { openWorkspace, waitForSettingsHydrated } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'

grokTest.describe('Grok Build session goal', () => {
  grokTest('sets, follows and clears a native goal', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    // The native fixture opens Grok in its default approval mode. This test needs `always-approve`, so it opens its own agent.
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT, { optionValues: { approvalMode: 'always-approve' } })
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

    const objective = scriptedObjective(modelScript, 'Keep the probe objective.')
    await submitGoal(page, objective.input)
    await expectGoalObjective(page, objective)

    await expectGoalStatus(page, 'paused')

    const callsBeforeResume = (await modelScript.status()).ruleMatches['every goal call answers with prose'] ?? 0
    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await expect.poll(async () => (await modelScript.status()).ruleMatches['every goal call answers with prose'] ?? 0).toBeGreaterThan(callsBeforeResume)
    await expectGoalStatus(page, 'paused')

    await clearGoal(page)
    await expectEmptyGoalCard(page)
  })
})

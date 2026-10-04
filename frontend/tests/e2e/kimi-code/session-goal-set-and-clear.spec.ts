import { expect } from '@playwright/test'
/**
 * The goal card sets and clears the actual native session goal. The test waits for authoritative Worker state.
 *
 * The Worker drives Kimi Code's kap-server protocol.
 *
 * The kap-server owns goal state and sends goal.updated events. LeapMux changes the session profile. Creating the goal starts no turn, so the Worker sends the objective as user input.
 */
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { completeGoalToolCall, createGoalToolCall } from '../helpers/providerToolCalls'
import { expandGoalsAndTodosSection } from '../helpers/subagentRegistry'
import { assistantBubbles, expectSettingsChip, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from '../helpers/ui'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

const KIMI = AgentProvider.KIMI_CODE

kimiTest.describe('Kimi Code session goal', () => {
  // The model can start a goal too. Outside Never Ask the server asks first,
  // and each answer can also switch the permission mode the goal runs in.
  kimiTest('a goal the model starts asks first, and the model can complete it', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Always Ask')

    await modelScript.queue(
      { toolCalls: [createGoalToolCall(KIMI, 'create-goal', 'Reply with KIMI_GOAL_DONE once.')] },
      { toolCalls: [completeGoalToolCall(KIMI, 'complete-goal')] },
      { text: 'KIMI_GOAL_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Start a goal to reply with KIMI_GOAL_DONE once, then complete it.'))
    await modelScript.waitForSteps(1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('CreateGoal')
    await page.getByTestId('control-more-actions').click()
    await page.getByTestId('control-decision-goal_mode:yolo').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
    // The answer switched the mode the goal runs in.
    await expectSettingsChip(page, 'Ask When Needed')

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'KIMI_GOAL_DONE' })).not.toHaveCount(0)
    // The server removes a completed goal, and the card follows it.
    await expandGoalsAndTodosSection(page)
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  })
})

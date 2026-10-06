import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expandGoalsAndTodosSection, expectEmptyGoalCard } from '../helpers/goalsAndTodos'
import { completeGoalToolCall, createGoalToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, controlActions, expectNoControlBanner, expectSettingsChip, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

const KIMI = AgentProvider.KIMI_CODE

/**
 * The goal card follows the actual native session goal. The test waits for authoritative Worker state.
 *
 * The Worker drives Kimi Code's kap-server protocol.
 *
 * The kap-server owns goal state and sends goal.updated events. LeapMux changes the session profile. Creating the goal starts no turn, so the Worker sends the objective as user input.
 */
kimiTest.describe('Kimi Code session goal', () => {
  // The model can start a goal too. Outside Never Ask the server asks first,
  // and each answer can also switch the permission mode that the goal runs in.
  kimiTest('a goal the model starts asks first, and the model can complete it', async ({ native }) => {
    const { page, modelScript } = native
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Always Ask')

    const start = await modelScript.queue(
      { toolCalls: [createGoalToolCall(KIMI, 'create-goal', 'Reply with KIMI_GOAL_DONE once.')] },
      { toolCalls: [completeGoalToolCall(KIMI, 'complete-goal')] },
      { text: 'KIMI_GOAL_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Start a goal to reply with KIMI_GOAL_DONE once, then complete it.'))
    await modelScript.waitForSteps(start + 1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('CreateGoal')
    // The composer draws the decision controls in its own fieldset, outside the banner.
    await controlActions(page).getByTestId('control-more-actions').click()
    await page.getByTestId('control-decision-goal_mode:yolo').filter({ visible: true }).click()
    await expectNoControlBanner(page)
    // The answer switched the mode that the goal runs in.
    await expectSettingsChip(page, 'Ask When Needed')

    await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'KIMI_GOAL_DONE' })).not.toHaveCount(0)
    // The server removes a completed goal, and the card follows it.
    await expandGoalsAndTodosSection(page)
    await expectEmptyGoalCard(page)
  })
})

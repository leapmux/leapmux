import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { blockGoalToolCall } from '../helpers/providerToolCalls'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction } from '../helpers/subagentRegistry'
import { messageBubbles, openWorkspace, waitForSettingsHydrated } from '../helpers/ui'
import { KIRO_E2E_SKIP_REASON, kiroTest, openKiroAgent } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

const KIRO = AgentProvider.KIRO

/**
 * The words each step of Kiro's goal workflow opens its prompt with. The step quotes
 * the user's `/goal` command there, and that command carries the marker.
 */
const GOAL_STEP_PROMPT = 'original_user_request'

kiroTest.describe('Kiro session goal', () => {
  // A step that reports an error fails the run. The goal card shows the words of
  // the step, not Kiro's generic reason for the failed node, and the words also
  // reach the parent transcript.
  kiroTest('leaves the goal blocked when a step reports an error', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { policyPreset: 'allow-all' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await modelScript.rule({
      name: 'the first step reports an error',
      when: { protocol: 'aws-event-stream', user: GOAL_STEP_PROMPT },
      respond: { toolCalls: [blockGoalToolCall(KIRO, 'kiro-goal-blocked', 'The repository is read-only.')] },
      once: true,
    })
    // The step then ends its turn with words, and Kiro wakes the parent session for
    // the step's message and for the failed run.
    await modelScript.fallback({ text: 'Recorded.' })

    await expandGoalsAndTodosSection(page)
    await goalAction(page, 'set').click()
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(modelScript.prompt('Rewrite the history.'))
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText('Rewrite the history.')

    await expectGoalStatus(page, 'blocked')
    await expect(page.locator('[data-testid="goal-status-detail"]:visible')).toContainText('The repository is read-only.')
    await expect(messageBubbles(page).filter({ hasText: 'The repository is read-only.' }).first()).toBeVisible()
  })
})

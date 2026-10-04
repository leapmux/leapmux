import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { completeGoalToolCall } from '../helpers/providerToolCalls'
import { expandGoalsAndTodosSection, expectGoalStatus, expectRegistryRow, goalAction } from '../helpers/subagentRegistry'
import { openWorkspace, waitForSettingsHydrated } from '../helpers/ui'
import { workflowGroupHeading, workflowRowsShareGroup } from '../helpers/workflowGrouping'
import { KIRO_E2E_SKIP_REASON, kiroTest, openKiroAgent } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

const KIRO = AgentProvider.KIRO

/**
 * The words each step of Kiro's goal workflow opens its prompt with. The step quotes
 * the user's `/goal` command there, and that command carries the marker.
 */
const GOAL_STEP_PROMPT = 'original_user_request'

kiroTest.describe('Kiro session goal', () => {
  kiroTest('sets a goal that a step completes, with the workflow in the registry', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { policyPreset: 'allow-all' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    // The first step reports success. Its session then ends its turn with words.
    // Kiro wakes the parent session once the workflow ends, and the number of
    // such turns is Kiro's own, so the fallback answers them.
    await modelScript.rule({
      name: 'the first step reports success',
      when: { protocol: 'aws-event-stream', user: GOAL_STEP_PROMPT },
      respond: { toolCalls: [completeGoalToolCall(KIRO, 'kiro-goal-done')] },
      once: true,
    })
    await modelScript.fallback({ text: 'Recorded.' })

    await expandGoalsAndTodosSection(page)
    await goalAction(page, 'set').click()
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(modelScript.prompt('Write the release notes.'))
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText('Write the release notes.')

    await expectGoalStatus(page, 'done')
    const step = await expectRegistryRow(page, { titleContains: 'goal · work #1' })
    const run = page
      .locator('[data-testid="bg-task-row"]:visible[data-kind="workflow"]')
      .filter({ hasText: 'Write the release notes.' })
      .first()
    await expect(run).toBeVisible()
    await expect(step).toHaveAttribute('data-kind', 'subagent')
    await expect.poll(() => workflowGroupHeading(run)).toBe('goal')
    await expect.poll(() => workflowGroupHeading(step)).toBe('goal')
    await expect.poll(() => workflowRowsShareGroup(run, step)).toBe(true)
  })
})

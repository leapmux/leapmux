import type { Page } from '@playwright/test'
import type { WorkspaceFixture } from '../helpers/workspace'
import { expect } from '@playwright/test'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { answerPlanReview, controlBanner, enterControlFeedback, expectSettingsChip, messageContents, openWorkspace, savedControlAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { mimoTest } from '../mimo-fixtures'
import { MIMO_AGENT } from './scenarios'

/**
 * Open a MiMo agent on its plan agent.
 *
 * MiMo's plan mode is a primary agent that each prompt runs on, and LeapMux
 * carries it on the permission-mode axis. `plan_exit` asks for approval only on
 * the plan agent: on any other agent it answers that plan mode is not active.
 */
async function openPlanAgent(page: Page, server: { hubUrl: string, adminToken: string, workerId: string }, workspace: WorkspaceFixture): Promise<void> {
  await openProviderAgent(server, workspace.workspaceId, MIMO_AGENT, { directoryPrefix: 'mimo-plan-', optionValues: { [OPTION_ID_PERMISSION_MODE]: 'plan' } })
  await openWorkspace(page, workspace.workspaceId)
  await expectSettingsChip(page, 'Plan')
}

mimoTest.describe('MiMo Code plan approval', () => {
  // An approval switches MiMo to the build agent. MiMo then writes its own
  // message that tells the model to execute the plan, and the loop goes on.
  mimoTest('an approved plan switches the agent to Build and the turn goes on', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openPlanAgent(page, leapmuxServer, authenticatedEmptyWorkspace)
    const start = await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(AgentProvider.MIMO_CODE, 'plan-exit', '')] },
      { text: 'PLAN_EXECUTION_STARTED' },
    )
    await sendMessage(page, modelScript.prompt('Finish the plan and ask for approval.'))
    await modelScript.waitForSteps(start + 1)

    const banner = controlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    // The script writes no plan file, so the worker has no plan text to show. The
    // banner states where MiMo keeps the plan instead.
    await expect(banner).toContainText(/Plan file: \S+\.md/)
    await answerPlanReview(page, 'approve')
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)

    await expect(messageContents(page).filter({ hasText: 'PLAN_EXECUTION_STARTED' }).first()).toBeVisible()
    await expectSettingsChip(page, 'Build')
    await expect(savedControlAnswer(page)).toHaveText('Approve')
  })

  // An empty rejection sends MiMo's native "No" answer.
  // The Plan agent remains selected. The plan call returns the rejection, and the same model loop continues.
  mimoTest('a plain rejection keeps the plan agent', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openPlanAgent(page, leapmuxServer, authenticatedEmptyWorkspace)
    const start = await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(AgentProvider.MIMO_CODE, 'plan-exit', '')] },
      { text: 'PLAN_KEPT' },
    )
    await sendMessage(page, modelScript.prompt('Finish the plan and ask for approval.'))
    await modelScript.waitForSteps(start + 1)

    const banner = controlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    await expect(page.getByTestId('plan-reject-btn').filter({ visible: true })).toHaveText('Reject')
    await answerPlanReview(page, 'reject')
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)

    await expect(messageContents(page).filter({ hasText: 'PLAN_KEPT' }).first()).toBeVisible()
    await expect(messageContents(page).filter({ hasText: 'Plan sent back' }).first()).toBeVisible()
    await expect(savedControlAnswer(page)).toHaveText('Reject')
    await expectSettingsChip(page, 'Plan')
  })

  // Words in the composer make the refusal a revision request. MiMo reads any
  // answer other than Yes or No as feedback, keeps the plan agent, and hands the
  // words to the model.
  mimoTest('feedback keeps the plan agent and reaches the model', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openPlanAgent(page, leapmuxServer, authenticatedEmptyWorkspace)
    const start = await modelScript.queue({ toolCalls: [exitPlanModeToolCall(AgentProvider.MIMO_CODE, 'plan-exit', '')] })
    await modelScript.rule({
      name: 'the model reads the plan feedback',
      when: { body: 'Split the migration into two steps' },
      respond: { text: 'PLAN_FEEDBACK_RECEIVED' },
      once: true,
    })
    await sendMessage(page, modelScript.prompt('Finish the plan and ask for approval.'))
    await modelScript.waitForSteps(start + 1)

    const banner = controlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    await enterControlFeedback(page, 'Split the migration into two steps')
    await expect(page.getByTestId('plan-reject-btn').filter({ visible: true })).toHaveText('Send feedback')
    await answerPlanReview(page, 'reject')
    await expect(banner).toHaveCount(0)
    await expect(messageContents(page).filter({ hasText: 'PLAN_FEEDBACK_RECEIVED' }).first()).toBeVisible()
    await waitForAgentIdle(page)
    await expectSettingsChip(page, 'Plan')
  })
})

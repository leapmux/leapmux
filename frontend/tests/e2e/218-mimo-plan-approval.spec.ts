import type { Page } from '@playwright/test'
import type { WorkspaceFixture } from './helpers/workspace'
import { OPTION_ID_PERMISSION_MODE } from '../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { openAgentViaAPI } from './helpers/api'
import { exitPlanModeToolCall } from './helpers/providerToolCalls'
import { createTestDirectory } from './helpers/runDirectory'
import { expectSettingsChip, messageContents, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, MIMO_E2E_SKIP_REASON, mimoTest } from './mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

/**
 * Open a MiMo agent on its plan agent.
 *
 * MiMo's plan mode is a primary agent that each prompt runs on, and LeapMux
 * carries it on the permission-mode axis. `plan_exit` asks for approval only on
 * the plan agent: on any other agent it answers that plan mode is not active.
 */
async function openPlanAgent(page: Page, server: { hubUrl: string, adminToken: string, workerId: string }, workspace: WorkspaceFixture): Promise<void> {
  const settings = agentOpenOptions(agentSettings(AgentProvider.MIMO_CODE))
  await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspace.workspaceId, createTestDirectory('mimo-plan-'), {
    agentProvider: AgentProvider.MIMO_CODE,
    ...settings,
    optionValues: { ...settings.optionValues, [OPTION_ID_PERMISSION_MODE]: 'plan' },
  })
  await openWorkspace(page, workspace.workspaceId)
  await expectSettingsChip(page, 'Plan')
}

mimoTest.describe('MiMo Code plan approval', () => {
  // An approval switches MiMo to the build agent. MiMo then writes its own
  // message that tells the model to execute the plan, and the loop goes on.
  mimoTest('an approved plan switches the agent to Build and the turn goes on', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openPlanAgent(page, leapmuxServer, authenticatedEmptyWorkspace)
    await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(AgentProvider.MIMO_CODE, 'plan-exit', '')] },
      { text: 'PLAN_EXECUTION_STARTED' },
    )
    await sendMessage(page, modelScript.prompt('Finish the plan and ask for approval.'))
    await modelScript.waitForSteps(1)

    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Plan Ready for Review')
    // The script writes no plan file, so the worker has no plan text to show. The
    // banner states where MiMo keeps the plan instead.
    await expect(banner).toContainText(/Plan file: \S+\.md/)
    await page.getByTestId('plan-approve-btn').click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(messageContents(page).filter({ hasText: 'PLAN_EXECUTION_STARTED' }).first()).toBeVisible()
    await expectSettingsChip(page, 'Build')
    await expect(page.locator('[data-testid="control-response-text"]:visible')).toHaveText('Approve')
  })

  // A rejection with no words is MiMo's own "No": the plan agent stays, the plan
  // call reads as sent back, and the loop goes on with the model.
  mimoTest('a plain rejection keeps the plan agent', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openPlanAgent(page, leapmuxServer, authenticatedEmptyWorkspace)
    await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(AgentProvider.MIMO_CODE, 'plan-exit', '')] },
      { text: 'PLAN_KEPT' },
    )
    await sendMessage(page, modelScript.prompt('Finish the plan and ask for approval.'))
    await modelScript.waitForSteps(1)

    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Plan Ready for Review')
    const reject = page.getByTestId('plan-reject-btn')
    await expect(reject).toHaveText('Reject')
    await reject.click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(messageContents(page).filter({ hasText: 'PLAN_KEPT' }).first()).toBeVisible()
    await expect(messageContents(page).filter({ hasText: 'Plan sent back' }).first()).toBeVisible()
    await expect(page.locator('[data-testid="control-response-text"]:visible')).toHaveText('Reject')
    await expectSettingsChip(page, 'Plan')
  })

  // Words in the composer make the refusal a revision request. MiMo reads any
  // answer other than Yes or No as feedback, keeps the plan agent, and hands the
  // words to the model.
  mimoTest('feedback keeps the plan agent and reaches the model', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openPlanAgent(page, leapmuxServer, authenticatedEmptyWorkspace)
    await modelScript.queue({ toolCalls: [exitPlanModeToolCall(AgentProvider.MIMO_CODE, 'plan-exit', '')] })
    await modelScript.rule({
      name: 'the model reads the plan feedback',
      when: { body: 'Split the migration into two steps' },
      respond: { text: 'PLAN_FEEDBACK_RECEIVED' },
      once: true,
    })
    await sendMessage(page, modelScript.prompt('Finish the plan and ask for approval.'))
    await modelScript.waitForSteps()

    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Plan Ready for Review')
    await page.getByTestId('composer-editor').locator('.ProseMirror').fill('Split the migration into two steps')
    const reject = page.getByTestId('plan-reject-btn')
    await expect(reject).toHaveText('Send feedback')
    await reject.click()
    await expect(banner).toHaveCount(0)
    await expect(messageContents(page).filter({ hasText: 'PLAN_FEEDBACK_RECEIVED' }).first()).toBeVisible()
    await waitForAgentIdle(page, 120_000)
    await expectSettingsChip(page, 'Plan')
  })
})

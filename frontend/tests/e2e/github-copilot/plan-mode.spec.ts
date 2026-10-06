import { expect } from '@playwright/test'
import { COPILOT_MODE, COPILOT_OPTION } from '../../../src/generated/contracts/copilot-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { copilotTest } from '../copilot-fixtures'
import { attachCopilotNativeLogs } from '../helpers/copilotNativeLogs'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, chooseSettingsOption, expectSettingsChip, messageBubbles, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'

copilotTest('uses the native exit tool and resumes after plan approval', async ({ authenticatedCopilotWorkspace, leapmuxServer, page, modelScript }, testInfo) => {
  void authenticatedCopilotWorkspace
  await chooseSettingsOption(page, `${COPILOT_OPTION.SessionMode}-${COPILOT_MODE.Plan}`)
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Plan')

  await modelScript.queue({ text: 'Plan mode is active.' })
  await sendMessage(page, modelScript.prompt('Confirm the selected mode.'))
  const first = await modelScript.waitForSteps(1)
  await waitForAgentIdle(page)
  expect(JSON.stringify(first.requests.find(request => request.stepIndex === 0)?.body)).toContain('"name":"exit_plan_mode"')

  await modelScript.queue(
    { toolCalls: [exitPlanModeToolCall(AgentProvider.GITHUB_COPILOT, 'copilot-plan', 'Review the Copilot change.')] },
    { text: 'The Copilot plan was approved.' },
  )
  await sendMessage(page, modelScript.prompt('Present the plan for approval.'))
  await modelScript.waitForSteps(2)
  await attachCopilotNativeLogs(leapmuxServer.agentEnv.COPILOT_HOME, testInfo)

  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('Proposed Plan')
  await expect(banner).toContainText('Review the Copilot change.')
  await expect(page.getByTestId('plan-approve-btn').filter({ visible: true })).toBeVisible()
  await page.getByTestId('plan-approve-btn').filter({ visible: true }).click()

  await modelScript.waitForSteps(3)
  await waitForAgentIdle(page)
  await expect(banner).toHaveCount(0)
  await expect(assistantBubbles(page).filter({ hasText: 'The Copilot plan was approved.' }).first()).toBeVisible()
  await page.reload()
  await expect(messageBubbles(page).filter({ hasText: 'Approved' }).first()).toBeVisible()
})

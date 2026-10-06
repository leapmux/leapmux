import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectSettingsChip, messageContents, sendMessage, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

codewhaleTest('enforces the actual Plan policy and changes to Agent without a plan review request', async ({ page, modelScript, leapmuxServer, authenticatedCodewhaleWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  await chooseSettingsOption(page, 'codewhale_mode-plan')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Plan')
  await expectNoNativeControl(context, { testId: 'plan-approve-btn', relatedControl: () => expectNoNativeControl(context, { testId: 'plan-reject-btn', relatedControl: async () => {
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEWHALE, 'no-review-plan', 'printf "PLANREFUSAL%s\\n" "$((40 + 2))"')] },
      { text: 'The native Plan policy refused shell execution.' },
    )
    await sendMessage(page, modelScript.prompt('Check the native Plan policy before execution.'))
    await waitForNativeToolSteps(context, 2)
    const planned = (await modelScript.status()).requests.find(request => request.stepIndex === 1)
    expect(nativeToolResult(planned, 'no-review-plan')).toContain('not available in Plan mode')
    await chooseSettingsOption(page, 'codewhale_mode-agent')
    await chooseSettingsOption(page, 'permissionMode-full_access')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Agent')
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEWHALE, 'no-review-agent', 'printf "PLANEXECUTION%s\\n" "$((40 + 2))"')] },
      { text: 'The actual Agent mode executed the shell command.' },
    )
    await sendMessage(page, modelScript.prompt('Execute the same operation after the native mode change.'))
    await waitForNativeToolSteps(context, 4)
    const executed = (await modelScript.status()).requests.find(request => request.stepIndex === 3)
    expect(nativeToolResult(executed, 'no-review-agent')).toContain('PLANEXECUTION42')
    await expect(messageContents(page).filter({ hasText: 'PLANEXECUTION42' }).first()).toBeVisible()
  } }) })
  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsChip(page, 'Agent')
  await expect(page.locator('[data-testid="plan-approve-btn"]:visible')).toHaveCount(0)
  await expect(page.locator('[data-testid="plan-reject-btn"]:visible')).toHaveCount(0)
})

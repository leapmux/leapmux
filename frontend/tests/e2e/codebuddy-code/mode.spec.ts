import { CODEBUDDY_MODE } from '../../../src/generated/contracts/codebuddy-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from '../codebuddy-fixtures'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, closeComposerMenus, expectSettingsChip, openSettingsMenu, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code settings', () => {
  codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

  codebuddyTest('the mode menu lists the four advertised modes', async ({ codebuddyWorkspace, page }) => {
    void codebuddyWorkspace
    await waitForSettingsHydrated(page)

    const group = await openSettingsMenu(page, 'permissionMode')
    for (const testId of [
      `permissionMode-${CODEBUDDY_MODE.Default}`,
      `permissionMode-${CODEBUDDY_MODE.AcceptEdits}`,
      `permissionMode-${CODEBUDDY_MODE.Plan}`,
      `permissionMode-${CODEBUDDY_MODE.BypassPermissions}`,
    ]) {
      await expect(group.locator(`[data-testid="${testId}"] input[type="radio"]`)).toBeVisible()
    }
    // The fixture opens the agent in Bypass Permissions.
    await expect(group.locator(`[data-testid="permissionMode-${CODEBUDDY_MODE.BypassPermissions}"] input[type="radio"]`)).toBeChecked()
    await closeComposerMenus(page)
  })

  codebuddyTest('keeps selected Plan mode for the next native turn', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await modelScript.queue({ text: 'The first mode probe answered.' })
    await sendMessage(page, modelScript.prompt('Reply before I change mode.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await chooseSettingsOption(page, `permissionMode-${CODEBUDDY_MODE.Plan}`)
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')
    await modelScript.queue({ toolCalls: [exitPlanModeToolCall(AgentProvider.CODEBUDDY, 'exit-selected-plan', '# Probe plan')] })
    await modelScript.fallback({ text: 'The mode probe ended.' })
    await sendMessage(page, modelScript.prompt('Present the plan for review after the mode change.'))
    const status = await modelScript.waitForSteps()
    const next = status.requests.find(request => request.stepIndex === 1)
    expect(next?.protocol).toBe('openai-chat-completions')
    expect(JSON.stringify(next?.body).includes('The first mode probe answered.')).toBe(true)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    await page.getByTestId('plan-reject-btn').click()
    await waitForAgentIdle(page)
    await expectSettingsChip(page, 'Plan')
  })
})

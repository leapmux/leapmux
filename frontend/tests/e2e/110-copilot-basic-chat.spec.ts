import { COPILOT_E2E_SKIP_REASON, copilotTest, expect } from './copilot-fixtures'
import { applyPermissionPreset, ARITHMETIC_PROMPT, expectAssistantAnswer, openPlusMenu, openSettingsMenu, sendMessage, waitForAgentIdle } from './helpers/ui'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

copilotTest.describe('Copilot Basic Chat', () => {
  copilotTest('send message and receive response', async ({ authenticatedCopilotWorkspace, page }) => {
    void authenticatedCopilotWorkspace
    await sendMessage(page, ARITHMETIC_PROMPT)
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page)
  })

  copilotTest('permission shortcuts switch Assisted Approval and Allow All', async ({ authenticatedCopilotWorkspace, page }) => {
    void authenticatedCopilotWorkspace
    // The Smart preset sets copilot_assisted_approval=on. A CLI that rejects
    // --assisted-approval leaves that group frozen to Off, and the shortcut must then be
    // absent -- asserted either way, so a shortcut that stops rendering cannot pass.
    const approvalGroup = await openSettingsMenu(page, 'copilot_assisted_approval')
    const assistedOffered = await approvalGroup
      .locator('[data-testid="copilot_assisted_approval-on"]')
      .count() > 0

    const menu = await openPlusMenu(page)
    const smart = menu.getByTestId('composer-smart-permissions')
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    if (assistedOffered)
      await expect(smart).toBeDisabled()
    else
      await expect(smart).toHaveCount(0)

    await applyPermissionPreset(page, 'bypass')
    let group = await openSettingsMenu(page, 'allow_all')
    await expect(group.locator('[data-testid="allow_all-on"] input[type="radio"]')).toBeChecked()
    group = await openSettingsMenu(page, 'copilot_assisted_approval')
    await expect(group.locator('[data-testid="copilot_assisted_approval-off"] input[type="radio"]')).toBeChecked()

    if (!assistedOffered)
      return

    await applyPermissionPreset(page, 'smart')
    group = await openSettingsMenu(page, 'copilot_assisted_approval')
    await expect(group.locator('[data-testid="copilot_assisted_approval-on"] input[type="radio"]')).toBeChecked()
    group = await openSettingsMenu(page, 'allow_all')
    await expect(group.locator('[data-testid="allow_all-off"] input[type="radio"]')).toBeChecked()
  })
})

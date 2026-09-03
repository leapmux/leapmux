import { COPILOT_E2E_SKIP_REASON, copilotTest, expect } from './copilot-fixtures'
import { ARITHMETIC_PROMPT, expectAssistantAnswer, openPlusMenu, openSettingsMenu, sendMessage, waitForAgentIdle, waitForSettingsIdle } from './helpers/ui'

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
    await openSettingsMenu(page, 'allow_all')
    let menu = await openPlusMenu(page)
    const smart = menu.getByTestId('composer-smart-permissions')
    const bypass = menu.getByTestId('composer-bypass-permissions')
    await expect(bypass).toBeVisible()
    const smartOffered = await smart.count() > 0
    if (smartOffered)
      await expect(smart).toBeDisabled()
    await bypass.click()
    await waitForSettingsIdle(page)

    let group = await openSettingsMenu(page, 'allow_all')
    await expect(group.locator('[data-testid="allow_all-on"] input[type="radio"]')).toBeChecked()
    group = await openSettingsMenu(page, 'copilot_assisted_approval')
    await expect(group.locator('[data-testid="copilot_assisted_approval-off"] input[type="radio"]')).toBeChecked()

    if (!smartOffered)
      return

    menu = await openPlusMenu(page)
    await menu.getByTestId('composer-smart-permissions').click()
    await waitForSettingsIdle(page)

    group = await openSettingsMenu(page, 'copilot_assisted_approval')
    await expect(group.locator('[data-testid="copilot_assisted_approval-on"] input[type="radio"]')).toBeChecked()
    group = await openSettingsMenu(page, 'allow_all')
    await expect(group.locator('[data-testid="allow_all-off"] input[type="radio"]')).toBeChecked()
  })
})

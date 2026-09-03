import { expect, GOOSE_E2E_SKIP_REASON, gooseTest } from './goose-fixtures'
import { applyPermissionPreset, ARITHMETIC_PROMPT, expectAssistantAnswer, openPlusMenu, openSettingsMenu, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from './helpers/ui'

gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')

gooseTest.describe('Goose Basic Chat', () => {
  gooseTest('send message and receive response', async ({ authenticatedGooseWorkspace, page }) => {
    void authenticatedGooseWorkspace
    await sendMessage(page, ARITHMETIC_PROMPT)
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page)
  })

  gooseTest('permission shortcuts switch Smart Approve and Auto', async ({ authenticatedGooseWorkspace, page }) => {
    void authenticatedGooseWorkspace
    await waitForSettingsHydrated(page)
    const menu = await openPlusMenu(page)
    // A new Goose session starts in Smart Approve, so the Smart shortcut is already
    // applied and therefore disabled.
    await expect(menu.getByTestId('composer-smart-permissions')).toBeDisabled()

    await applyPermissionPreset(page, 'bypass')
    let group = await openSettingsMenu(page, 'permissionMode')
    await expect(group.locator('[data-testid="permissionMode-auto"] input[type="radio"]')).toBeChecked()

    await applyPermissionPreset(page, 'smart')
    group = await openSettingsMenu(page, 'permissionMode')
    await expect(group.locator('[data-testid="permissionMode-smart_approve"] input[type="radio"]')).toBeChecked()
  })
})

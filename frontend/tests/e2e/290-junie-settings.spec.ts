import { JUNIE_MOCK_MODEL } from './helpers/mockAgentEnvironment'
import {
  chooseSettingsOption,
  closeComposerMenus,
  expectSettingsChip,
  openPlusMenu,
  openSettingsMenu,
  settingsGroupTrigger,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest } from './junie-fixtures'

junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

junieTest.describe('Junie settings', () => {
  // Junie's mode axis is its `mode` config option: Default and Plan. A new
  // session runs Default, so the mode menu shows Default checked and Plan as
  // the other choice. The model comes from the custom-model profile the
  // environment writes, and the effort axis is the well-known `effort` id.
  junieTest('the settings menu offers the model, effort, and mode axes', async ({ authenticatedJunieWorkspace, page }) => {
    void authenticatedJunieWorkspace
    await waitForSettingsHydrated(page)

    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'permissionMode')).toBeVisible()
    await closeComposerMenus(page)

    const mode = await openSettingsMenu(page, 'permissionMode')
    await expect(mode.locator('[data-testid="permissionMode-default"] input[type="radio"]')).toBeChecked()
    await expect(mode.locator('[data-testid="permissionMode-plan"] input[type="radio"]')).toBeVisible()
    await closeComposerMenus(page)

    // Junie's `effort` config option takes low, medium or high.
    const effort = await openSettingsMenu(page, 'effort')
    await expect(effort.locator('[data-testid="effort-low"]')).toBeVisible()
    await expect(effort.locator('[data-testid="effort-medium"]')).toBeVisible()
    await expect(effort.locator('[data-testid="effort-high"]')).toBeVisible()
    await closeComposerMenus(page)

    // The pinned custom-model profile is the only model the session lists.
    const model = await openSettingsMenu(page, 'model')
    await expect(model.locator(`[data-testid="model-${JUNIE_MOCK_MODEL}"]`).first()).toBeVisible()
    await closeComposerMenus(page)
  })

  // Plan mode is a mode config option for Junie (matrix note 27), not a tool.
  // Choosing it writes the option live and the chip follows the selection.
  junieTest('a mode switch to Plan reaches the chip and survives a reload', async ({ authenticatedJunieWorkspace, page }) => {
    void authenticatedJunieWorkspace
    await waitForSettingsHydrated(page)

    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
  })
})

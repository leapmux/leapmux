import { JUNIE_MOCK_MODEL } from '../helpers/mockAgentEnvironment'
import { chooseSettingsOption, closeComposerMenus, expectSettingsChip, openPlusMenu, openSettingsMenu, settingsGroupTrigger, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expect, junieTest } from '../junie-fixtures'
import { exerciseNativePlanReview, exerciseNativePlanRevision, expectNativePlanToolCatalog } from './planScenarios'
import { nativeContext } from './scenarios'

junieTest.describe('Junie settings', () => {
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

    // Junie's effort option accepts these values:
    // - low
    // - medium
    // - high
    const effort = await openSettingsMenu(page, 'effort')
    await expect(effort.locator('[data-testid="effort-low"]')).toBeVisible()
    await expect(effort.locator('[data-testid="effort-medium"]')).toBeVisible()
    await expect(effort.locator('[data-testid="effort-high"]')).toBeVisible()
    await closeComposerMenus(page)

    // The session lists the pinned custom model profile.
    const model = await openSettingsMenu(page, 'model')
    await expect(model.locator(`[data-testid="model-${JUNIE_MOCK_MODEL}"]`).first()).toBeVisible()
    await closeComposerMenus(page)
  })

  junieTest('a mode switch to Plan reaches the chip and survives a reload', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
    await waitForSettingsHydrated(page)

    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    // The first review denies its plan. Approval ends Plan mode in Junie, and Junie can then answer the
    // next planning prompt with the question "You already have a plan in this session". That prompt waits
    // for a reply and sends no model request. A denied plan keeps Junie in Plan mode and raises no question.
    const selected = await exerciseNativePlanRevision(context, { callPrefix: 'junie-selected-mode' })
    expectNativePlanToolCatalog(selected)

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    const restored = await exerciseNativePlanReview(context, { selectMode: false, callPrefix: 'junie-restored-mode', housekeepingRegistered: true })
    expectNativePlanToolCatalog(restored)
  })
})

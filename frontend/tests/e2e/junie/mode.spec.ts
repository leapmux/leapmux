import { JUNIE_MODE } from '../../../src/generated/contracts/junie-protocol'
import { JUNIE_MOCK_MODEL } from '../helpers/mockAgentEnvironment'
import { expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { chooseSettingsOption, closeComposerMenus, expectSettingsChip, expectSettingsOptionChosen, offeredSettingsOptions, openPlusMenu, settingsGroupTrigger, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expect, junieTest } from '../junie-fixtures'
import { exerciseNativePlanReview, exerciseNativePlanRevision, expectNativePlanToolCatalog } from './planScenarios'

junieTest.describe('Junie settings', () => {
  junieTest('the settings menu offers the model, effort, and mode axes', async ({ authenticatedJunieWorkspace, page }) => {
    void authenticatedJunieWorkspace
    await waitForSettingsHydrated(page)

    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'permissionMode')).toBeVisible()
    await closeComposerMenus(page)

    await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(JUNIE_MODE))
    await expectSettingsOptionChosen(page, `permissionMode-${JUNIE_MODE.Default}`)

    // Junie's effort option accepts these values:
    // - low
    // - medium
    // - high
    await expectSettingsOptionsOffered(page, 'effort', ['low', 'medium', 'high'])

    // The session lists the pinned custom model profile.
    expect(await offeredSettingsOptions(page, 'model')).toContain(JUNIE_MOCK_MODEL)
  })

  junieTest('a mode switch to Plan reaches the chip and survives a reload', async ({ native: context, page }) => {
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
    const restored = await exerciseNativePlanReview(context, { selectMode: false, callPrefix: 'junie-restored-mode' })
    expectNativePlanToolCatalog(restored)
  })
})

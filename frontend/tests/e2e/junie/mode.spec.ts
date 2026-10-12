import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { JUNIE_MODE } from '../../../src/generated/contracts/junie-protocol'
import { JUNIE_MOCK_MODEL } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseRestoredNativeOption, expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { closeComposerMenus, expectSettingsChip, expectSettingsOptionChosen, offeredSettingsOptions, openPlusMenu, settingsGroupTrigger, waitForSettingsHydrated } from '../helpers/ui'
import { junieTest } from '../junie-fixtures'
import { exerciseNativePlanReview, exerciseNativePlanRevision, expectNativePlanToolCatalog } from './planScenarios'
import { PLAN_MODE_WITHHELD_TOOLS, planWithheldToolsOffered } from './planToolCatalog'

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

    const notice = await chooseNativeModeWithNotice(context, {
      groupId: 'permissionMode',
      value: JUNIE_MODE.Plan,
      label: 'Plan',
      previousLabel: 'Default',
      noticeLabel: 'Mode',
    })

    // Deny the first plan. Approval ends Plan mode, and the next planning prompt can ask about the approved plan.
    // That question waits for a reply and sends no model request. A denied plan keeps Plan mode and raises no question.
    const selected = await exerciseNativePlanRevision(context, { callPrefix: 'junie-selected-mode' })
    expectNativePlanToolCatalog(selected)

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    const restored = await exerciseNativePlanReview(context, { selectMode: false, callPrefix: 'junie-restored-mode' })
    expectNativePlanToolCatalog(restored)
    await notice.assertStored()
  })

  junieTest('returns through the menu from Plan to the native Default tool catalog', async ({ native, page }) => {
    await waitForSettingsHydrated(page)
    await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(JUNIE_MODE))
    const planNotice = await chooseNativeModeWithNotice(native, {
      groupId: 'permissionMode',
      value: JUNIE_MODE.Plan,
      label: 'Plan',
      previousLabel: 'Default',
      noticeLabel: 'Mode',
    })
    expectNativePlanToolCatalog(await exerciseNativePlanRevision(native, { callPrefix: 'junie-return-mode' }))
    const defaultNotice = await chooseNativeModeWithNotice(native, {
      groupId: 'permissionMode',
      value: JUNIE_MODE.Default,
      label: 'Default',
      previousLabel: 'Plan',
      noticeLabel: 'Mode',
    })
    const nativeProof = (request: MockModelRequestRecord) => {
      expect(planWithheldToolsOffered(nativeModelToolNames(request))).toEqual([...PLAN_MODE_WITHHELD_TOOLS])
    }
    nativeProof(await sendNativeAnswer(native, 'Reply once after returning to Default.', 'The native Default catalog answered.'))
    await exerciseRestoredNativeOption(native, { groupId: 'permissionMode', value: JUNIE_MODE.Default, nativeProof })
    await expectSettingsChip(page, 'Default')
    await planNotice.assertStored()
    await defaultNotice.assertStored()
  })
})

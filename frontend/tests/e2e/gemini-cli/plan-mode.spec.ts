import { expect } from '@playwright/test'
import { geminiTest } from '../gemini-fixtures'
import { exerciseNativeReadOnlyPlan } from '../helpers/nativeReadOnlyPlan'
import { expectNativeOptionValue } from '../helpers/nativeScenario'
import { assistantBubbles, chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

geminiTest('reads real file context through native plan mode after reload', async ({ native, page }) => {
  await exerciseNativeReadOnlyPlan(native, {
    preparePlan: async () => {
      await chooseSettingsOption(page, 'permissionMode-plan')
      await waitForSettingsIdle(page)
    },
    nativeProof: async (request) => {
      expect(request.protocol).toBe('google-generative-language')
      await expectNativeOptionValue(native, 'permissionMode', 'plan')
    },
  })
  await expect(assistantBubbles(page).filter({ hasText: 'Implement after the user selects execution mode.' })).toBeVisible()
  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsOptionChosen(page, 'permissionMode-plan')
})

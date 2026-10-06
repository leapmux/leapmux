import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { exerciseNativeReadOnlyPlan } from '../helpers/nativeReadOnlyPlan'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { chooseSettingsOption, PLAN_REVIEW_BUTTON_TEST_IDS, waitForSettingsIdle } from '../helpers/ui'

commandCodeTest('returns a native read-only plan without a plan approval dialog', async ({ native, page }) => {
  await expectNoNativeControl(native, { testId: 'control-banner', additionalTestIds: [...PLAN_REVIEW_BUTTON_TEST_IDS], relatedProof: async () => {
    await exerciseNativeReadOnlyPlan(native, {
      preparePlan: async () => {
        await chooseSettingsOption(page, 'permissionMode-plan')
        await waitForSettingsIdle(page)
      },
      nativeProof: request => expect(nativeModelInstructionText(request)).toMatch(/plan[\s\S]*(?:read-only|read only)/i),
    })
  } })
})

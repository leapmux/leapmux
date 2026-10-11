import { expect } from '@playwright/test'
import { exerciseNativeReadOnlyPlan } from '../helpers/nativeReadOnlyPlan'
import { expectNativeOptionValue, nativeModelContextText } from '../helpers/nativeScenario'
import { answerControl, chooseSettingsOption, expectNoControlBanner, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'
import { expectNoPlanReview } from '../helpers/unsupportedPlanMode'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('uses the native exit permission without a dedicated plan approval banner', async ({ native }) => {
  const { page } = native
  await expectNoPlanReview(native, {
    relatedProof: () => exerciseNativeReadOnlyPlan(native, {
      preparePlan: async () => {
        await chooseSettingsOption(page, 'permissionMode-plan')
        await waitForSettingsIdle(page)
      },
      nativeProof: async (request) => {
        expect(nativeModelContextText(request).toLowerCase()).toContain('plan mode')
        const banner = await waitForControlBanner(page)
        await expect(banner).toContainText('exit_plan_mode')
        await answerControl(page, 'deny')
        await expectNoControlBanner(page)
      },
    }),
  })
  await expectNativeOptionValue(native, 'permissionMode', 'plan')
})

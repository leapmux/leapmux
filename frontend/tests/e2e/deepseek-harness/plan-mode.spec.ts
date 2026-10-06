import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseNativeReadOnlyPlan } from '../helpers/nativeReadOnlyPlan'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'

deepseekHarnessTest('uses the native plan mode after a real file read', async ({ native, page }) => {
  await exerciseNativeReadOnlyPlan(native, {
    preparePlan: async () => {
      await chooseSettingsOption(page, 'permissionMode-plan')
      await waitForSettingsIdle(page)
    },
    nativeProof: request => expect(nativeModelInstructionText(request)).toContain('You are in plan mode.'),
  })
})

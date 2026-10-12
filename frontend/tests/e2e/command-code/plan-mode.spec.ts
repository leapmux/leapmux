import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { exerciseNativeReadOnlyPlan } from '../helpers/nativeReadOnlyPlan'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'

commandCodeTest('reads actual file context in the native planning mode', async ({ native, page }) => {
  await exerciseNativeReadOnlyPlan(native, {
    preparePlan: async () => {
      await chooseSettingsOption(page, 'permissionMode-plan')
      await waitForSettingsIdle(page)
    },
    nativeProof: request => expect(nativeModelInstructionText(request)).toMatch(/plan[\s\S]*(?:read-only|read only)/i),
  })
})

import { expect } from '@playwright/test'
import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseNativePlanInstructions } from '../helpers/nativeReadOnlyPlan'
import { codebuddyPlanOptionSnapshot } from './planMode'

codebuddyTest('adds actual native planning instructions and preserves the selected mode after reload', async ({ native }) => {
  await exerciseNativePlanInstructions(native, {
    keptSettings: (before, after) => expect(codebuddyPlanOptionSnapshot(after.optionGroups)).toEqual(codebuddyPlanOptionSnapshot(before.optionGroups)),
  })
})

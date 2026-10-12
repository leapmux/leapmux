import { expect } from '@playwright/test'
import { exerciseNativePlanInstructions } from '../helpers/nativePlanMode'
import { nativeOptionValue } from '../helpers/nativeScenario'
import { qoderTest } from '../qoder-fixtures'

qoderTest('adds actual native planning instructions and preserves the selected mode after reload', async ({ native }) => {
  await exerciseNativePlanInstructions(native, {
    keptSettings: (before, after) => {
      for (const groupId of ['model', 'effort']) {
        const original = nativeOptionValue(before, groupId)
        if (!original)
          throw new Error(`The native ${groupId} catalog is absent.`)
        expect(nativeOptionValue(after, groupId), `the Plan mode keeps the ${groupId}`).toBe(original)
      }
    },
  })
})

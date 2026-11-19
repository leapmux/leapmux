import { gooseTest } from '../goose-fixtures'
import { applyPermissionPreset, waitForSettingsIdle } from '../helpers/ui'
import { exerciseMissingNativePlanMode, expectNoPlanReview } from '../helpers/unsupportedPlanMode'

gooseTest('runs a native tool turn after a literal plan command without plan approval', async ({ native }) => {
  await applyPermissionPreset(native.page, 'bypass')
  await waitForSettingsIdle(native.page)
  await expectNoPlanReview(native, {
    relatedProof: () => exerciseMissingNativePlanMode(native, { reload: false }),
  })
})

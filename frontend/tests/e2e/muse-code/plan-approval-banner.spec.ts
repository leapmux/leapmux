import { exerciseMissingNativePlanMode, expectNoPlanReview } from '../helpers/unsupportedPlanMode'
import { museTest } from '../muse-fixtures'

museTest('runs the actual no-plan native route without a plan review request', async ({ native }) => {
  await expectNoPlanReview(native, { relatedProof: () => exerciseMissingNativePlanMode(native, { reload: false }) })
})

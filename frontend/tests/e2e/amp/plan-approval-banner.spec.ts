import { ampTest } from '../amp-fixtures'
import { exerciseMissingNativePlanMode, expectNoPlanReview } from '../helpers/unsupportedPlanMode'

ampTest('runs the actual no-plan native route without a plan review request', async ({ native }) => {
  await expectNoPlanReview(native, { relatedProof: () => exerciseMissingNativePlanMode(native, { reload: false }) })
})

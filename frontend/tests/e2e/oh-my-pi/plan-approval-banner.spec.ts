import { exerciseMissingNativePlanMode, expectNoPlanReview } from '../helpers/unsupportedPlanMode'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('runs the actual no-plan native route without a plan review request', async ({ native }) => {
  await expectNoPlanReview(native, { relatedProof: () => exerciseMissingNativePlanMode(native, { reload: false }) })
})

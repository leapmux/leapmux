import { gooseTest } from '../goose-fixtures'
import { expectNoPlanReview } from '../helpers/unsupportedPlanMode'
import { exerciseGoosePlanLimit } from './planLimitScenario'

gooseTest('keeps native Chat tool refusal separate from a plan approval banner', async ({ native }) => {
  await expectNoPlanReview(native, { relatedProof: () => exerciseGoosePlanLimit(native) })
})

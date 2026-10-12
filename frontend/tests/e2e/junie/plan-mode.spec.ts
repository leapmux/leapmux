import { junieTest } from '../junie-fixtures'
import { exerciseNativePlanReview, expectNativePlanToolCatalog } from './planScenarios'

junieTest('uses the native planning tool catalog after selecting Plan mode', async ({ native }) => {
  const request = await exerciseNativePlanReview(native)
  expectNativePlanToolCatalog(request)
})

import { junieTest } from '../junie-fixtures'
import { exerciseNativePlanReview, expectNativePlanToolCatalog } from './planScenarios'
import { nativeContext } from './scenarios'

junieTest('uses the native planning tool catalog after selecting Plan mode', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  const request = await exerciseNativePlanReview(context)
  expectNativePlanToolCatalog(request)
})

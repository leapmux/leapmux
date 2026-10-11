import { piTest } from '../pi-fixtures'
import { exercisePiFreshPlanSession } from './planScenario'
import { nativeContext } from './scenarios'

piTest('tracks a fresh Pi implementation session after plan approval', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  await exercisePiFreshPlanSession(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId }))
})

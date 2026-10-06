import { qoderTest } from '../qoder-fixtures'
import { exerciseNativeGoalCycle } from './goalScenarios'
import { nativeContext } from './scenarios'

qoderTest('sets and clears an actual native goal without a model turn', async ({ authenticatedQoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedQoderWorkspace.workspaceId })
  await exerciseNativeGoalCycle(context)
})

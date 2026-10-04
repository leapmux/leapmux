import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseQueuedTurnWithoutSteering } from '../helpers/nativeToolSteering'
import { nativeContext } from './scenarios'

fastAgentTest('refuses mid-turn steering and runs the queued prompt in a separate native turn', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await exerciseQueuedTurnWithoutSteering(context)
})

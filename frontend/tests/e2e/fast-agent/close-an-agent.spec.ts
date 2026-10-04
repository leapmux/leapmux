import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

fastAgentTest('closes the native provider and its owned tool process', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await exerciseCloseAgent(context)
})

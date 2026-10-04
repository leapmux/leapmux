import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'
import { nativeContext } from './scenarios'

fastAgentTest('shows the native model error and runs a later valid turn', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await exerciseModelError(context)
})

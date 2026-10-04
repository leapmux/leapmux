import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

fastAgentTest('reopens a completed picker session and restores its saved Worker rows', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await exerciseSessionResume(context)
})

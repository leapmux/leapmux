import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { nativeContext } from './scenarios'

fastAgentTest('runs successful and failed native commands with their actual output', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await exerciseShellToolExecution(context)
})

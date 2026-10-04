import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeContext, nativeLaunch } from './scenarios'

fastAgentTest('delivers input queued while the actual native process starts', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context), failed: false })
})

fastAgentTest('keeps queued input when the actual native launch fails', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context), failed: true })
})

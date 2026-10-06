import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeContext, nativeLaunch } from './scenarios'

codebuddyTest('delivers input queued while the actual native process starts', async ({ authenticatedCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodebuddyWorkspace.workspaceId })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context), failed: false })
})

codebuddyTest('keeps queued input when the actual native launch fails', async ({ authenticatedCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodebuddyWorkspace.workspaceId })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context), failed: true })
})

import { droidTest } from '../droid-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeContext, nativeLaunch } from './scenarios'

droidTest('delivers input queued while the actual native process starts', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context), failed: false })
})

droidTest('keeps queued input when the actual native launch fails', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context), failed: true })
})

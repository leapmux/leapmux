import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { junieTest } from '../junie-fixtures'
import { nativeContext, nativeLaunch } from './scenarios'

junieTest('delivers input queued while the actual native process starts', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context), failed: false })
})

junieTest('keeps queued input when the actual native launch fails', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context), failed: true })
})

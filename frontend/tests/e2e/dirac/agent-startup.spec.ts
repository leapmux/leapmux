import { diracTest } from '../dirac-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeContext, nativeLaunch } from './scenarios'

diracTest('delivers input queued while the actual native process starts', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context), failed: false })
})

diracTest('keeps queued input when the actual native launch fails', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context), failed: true })
})

import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { lettaTest } from '../letta-fixtures'
import { nativeContext, nativeLaunch } from './scenarios'

lettaTest('delivers input queued while the actual native process starts', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context), failed: false })
})

lettaTest('keeps queued input when the actual native launch fails', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context), failed: true })
})

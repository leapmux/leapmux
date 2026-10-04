import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { qoderTest } from '../qoder-fixtures'
import { nativeContext, nativeLaunch } from './scenarios'

qoderTest('delivers input queued while the actual native process starts', async ({ qoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: qoderWorkspace.workspaceId })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context), failed: false })
})

qoderTest('keeps queued input when the actual native launch fails', async ({ qoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: qoderWorkspace.workspaceId })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context), failed: true })
})

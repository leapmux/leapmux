import { droidTest } from '../droid-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { nativeContext } from './scenarios'

droidTest('exposes no token or byte counter throughout the completed native stream', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await exerciseGenerationProgress(context, { supported: false, counter: 'tokens', approveTool: false })
})

droidTest('reports no byte count throughout an actual native shell output stream', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await exerciseGenerationProgress(context, { supported: false, counter: 'bytes' })
})

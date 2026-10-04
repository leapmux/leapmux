import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { nativeContext } from './scenarios'

fastAgentTest('reports an advancing token count while native output arrives', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens', approveTool: true })
})

fastAgentTest('reports an advancing byte count while native output arrives', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await exerciseGenerationProgress(context, { supported: true, counter: 'bytes', approveTool: true })
})

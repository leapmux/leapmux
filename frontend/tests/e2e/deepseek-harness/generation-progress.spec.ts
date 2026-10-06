import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { nativeContext } from './scenarios'

deepseekHarnessTest('counts advancing native model chunks and retains their completed output', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens', approveTool: false })
})

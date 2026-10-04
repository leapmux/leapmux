import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { nativeContext } from './scenarios'

deepseekHarnessTest('counts advancing native model chunks and retains their completed output', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens', approveTool: false })
})

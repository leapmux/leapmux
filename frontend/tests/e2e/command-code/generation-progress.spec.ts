import { commandCodeTest } from '../command-code-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { nativeContext } from './scenarios'

commandCodeTest('counts native model and command bytes and retains the completed result', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens', approveTool: false })
  await exerciseGenerationProgress(context, { supported: true, counter: 'bytes' })
})

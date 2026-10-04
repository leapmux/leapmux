import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { nativeContext } from './scenarios'

codebuddyTest('exposes no token or byte counter throughout the completed native stream', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
  await exerciseGenerationProgress(context, { supported: false, counter: 'tokens', approveTool: false })
})

codebuddyTest('reports no byte count throughout an actual native shell output stream', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
  await exerciseGenerationProgress(context, {
    supported: false,
    counter: 'bytes',
    prepareCompletedResultView: async (callId) => {
      const result = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${callId}"][data-tool-row-role="result"]:visible`)
      await expandNativeResultView(result)
    },
  })
})

import { geminiTest } from '../gemini-fixtures'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { nativeContext } from './scenarios'

geminiTest('reads and changes actual native files and shows the applied diff', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseFileToolExecution(context)
})

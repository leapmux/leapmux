import { geminiTest } from '../gemini-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { expectGeminiLiveChild, finishGeminiChildWithReload, openGeminiRunningChild } from './childScenarios'
import { nativeContext } from './scenarios'

geminiTest('shows original child content and tool output before the native child completes', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const child = await openGeminiRunningChild(context)
  await withCleanup(async () => {
    await expectGeminiLiveChild(context, child)
    await finishGeminiChildWithReload(context, child)
  }, child.finish)
})

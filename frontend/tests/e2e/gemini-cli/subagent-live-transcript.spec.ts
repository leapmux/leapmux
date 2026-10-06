import { geminiTest } from '../gemini-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { expectGeminiLiveChild, finishGeminiChildWithReload, openGeminiRunningChild } from './childScenarios'

geminiTest('shows original child content and tool output before the native child completes', async ({ native }) => {
  const child = await openGeminiRunningChild(native)
  await withCleanup(async () => {
    await expectGeminiLiveChild(native, child)
    await finishGeminiChildWithReload(native, child)
  }, child.finish)
})

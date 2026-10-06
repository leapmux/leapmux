import { geminiTest } from '../gemini-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

geminiTest('exposes no separate native output style setting', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await expectMissingOptionGroup(context, { groupId: 'outputStyle', relatedProof: async () => {
    await sendNativeAnswer(context, 'Complete the native setting capability check.', 'The native setting capability check completed.')
  } })
})

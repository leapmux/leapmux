import { geminiTest } from '../gemini-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

geminiTest('exposes no separate native fast serving setting', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await expectMissingOptionGroup(context, { groupId: 'fastMode', relatedProof: async () => {
    await sendNativeAnswer(context, 'Complete the native setting capability check.', 'The native setting capability check completed.')
  } })
})

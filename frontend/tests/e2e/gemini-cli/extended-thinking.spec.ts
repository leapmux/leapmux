import { GEMINI_E2E_SKIP_REASON, geminiTest } from '../gemini-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest('exposes no separate native setting for extended thinking', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await expectMissingOptionGroup(context, { groupId: 'extendedThinking', relatedProof: async () => {
    await sendNativeAnswer(context, 'Complete the native setting capability check.', 'The native setting capability check completed.')
  } })
})

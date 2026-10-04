import { fastAgentTest } from '../fastagent-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

fastAgentTest('exposes no native reasoning effort setting', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await expectMissingOptionGroup(context, { groupId: 'effort', relatedProof: async () => {
    await sendNativeAnswer(context, 'Complete the native effort catalog probe.', 'The native effort catalog probe completed.')
  } })
})

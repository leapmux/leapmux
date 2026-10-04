import { sendNativeAnswer } from '../helpers/nativeConversation'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('exposes no separate native extended thinking setting', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await expectMissingOptionGroup(context, { groupId: 'thinking', relatedProof: async () => {
    await sendNativeAnswer(context, 'Complete the native setting capability probe.', 'The native setting capability probe completed.')
  } })
})

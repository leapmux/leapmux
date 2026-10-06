import { codebuddyTest } from '../codebuddy-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

codebuddyTest('exposes no separate native extended thinking setting', async ({ authenticatedCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodebuddyWorkspace.workspaceId })
  await expectMissingOptionGroup(context, { groupId: 'thinking', relatedProof: async () => {
    await sendNativeAnswer(context, 'Complete the native setting capability probe.', 'The native setting capability probe completed.')
  } })
})

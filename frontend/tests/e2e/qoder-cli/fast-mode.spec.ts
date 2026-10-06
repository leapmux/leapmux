import { sendNativeAnswer } from '../helpers/nativeConversation'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest('exposes no separate native fast mode setting', async ({ authenticatedQoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedQoderWorkspace.workspaceId })
  await expectMissingOptionGroup(context, { groupId: 'fast_mode', relatedProof: async () => {
    await sendNativeAnswer(context, 'Complete the native setting capability probe.', 'The native setting capability probe completed.')
  } })
})

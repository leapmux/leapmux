import { sendNativeAnswer } from '../helpers/nativeConversation'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('exposes no native reasoning effort setting', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await expectMissingOptionGroup(context, { groupId: 'effort', relatedProof: async () => {
    await sendNativeAnswer(context, 'Complete the native effort catalog probe.', 'The native effort catalog probe completed.')
  } })
})

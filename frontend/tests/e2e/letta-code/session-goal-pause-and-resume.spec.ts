import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('refuses only the unsupported native goal actions and preserves the supported actions', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await expectUnsupportedGoalActions(context, { actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME], relatedProof: async () => {
    await sendNativeAnswer(context, 'Complete before the native goal refusal.', 'The native goal probe completed.')
  } })
})

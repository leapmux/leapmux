import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { diracTest } from '../dirac-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

diracTest('refuses only the unsupported native goal actions and preserves the supported actions', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await expectUnsupportedGoalActions(context, { actions: [AgentGoalAction.SET, AgentGoalAction.CLEAR], relatedProof: async () => {
    await sendNativeAnswer(context, 'Complete before the native goal refusal.', 'The native goal probe completed.')
  } })
})

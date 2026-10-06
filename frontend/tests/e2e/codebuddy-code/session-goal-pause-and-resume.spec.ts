import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest } from '../codebuddy-fixtures'
import { expectGoalStatus, submitGoal } from '../helpers/goalsAndTodos'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

codebuddyTest('refuses only the unsupported native goal actions and preserves the supported actions', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
  await expectUnsupportedGoalActions(context, { actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME], relatedProof: async () => {
    await sendNativeAnswer(context, 'Complete before the native goal refusal.', 'The native goal probe completed.')
    await modelScript.rule({ name: 'native-negative-goal-command', when: { user: '<user_query>/goal' }, respond: { text: 'The native goal command completed.' } })
    await submitGoal(page, 'Keep the native goal active for its pause refusal.')
    await expectGoalStatus(page, 'active')
  } })
})

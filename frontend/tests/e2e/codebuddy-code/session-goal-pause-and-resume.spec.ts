import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest } from '../codebuddy-fixtures'
import { expectGoalStatus, nativeGoalProbeTurn, submitGoal } from '../helpers/goalsAndTodos'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'

codebuddyTest('refuses only the unsupported native goal actions and preserves the supported actions', async ({ native }) => {
  const { page, modelScript } = native
  await expectUnsupportedGoalActions(native, { actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME], relatedProof: async () => {
    await nativeGoalProbeTurn(native)
    await modelScript.rule({ name: 'native-negative-goal-command', when: { user: '<user_query>/goal' }, respond: { text: 'The native goal command completed.' } })
    await submitGoal(page, 'Keep the native goal active for its pause refusal.')
    await expectGoalStatus(page, 'active')
  } })
})

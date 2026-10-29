import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest } from '../codebuddy-fixtures'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { relatedNativeProof } from './scenarios'

codebuddyTest('refuses unsupported goal creation and clearing on the running native provider', async ({ native }) => {
  await expectUnsupportedGoalActions(native, {
    actions: [
      AgentGoalAction.SET,
      AgentGoalAction.CLEAR,
      AgentGoalAction.PAUSE,
      AgentGoalAction.RESUME,
    ],
    relatedProof: () => relatedNativeProof(native),
  })
})

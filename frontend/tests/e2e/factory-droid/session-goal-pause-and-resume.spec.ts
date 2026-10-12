import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { droidTest } from '../droid-fixtures'
import { nativeGoalProbeTurn } from '../helpers/goalsAndTodos'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'

droidTest('refuses only the unsupported native goal actions and preserves the supported actions', async ({ native }) => {
  await expectUnsupportedGoalActions(native, { actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME], relatedProof: async () => {
    await nativeGoalProbeTurn(native)
  } })
})

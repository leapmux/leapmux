import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { diracTest } from '../dirac-fixtures'
import { nativeGoalProbeTurn } from '../helpers/goalsAndTodos'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'

diracTest('refuses only the unsupported native goal actions and preserves the supported actions', async ({ native }) => {
  await expectUnsupportedGoalActions(native, { actions: [AgentGoalAction.SET, AgentGoalAction.CLEAR], relatedProof: async () => {
    await nativeGoalProbeTurn(native)
  } })
})

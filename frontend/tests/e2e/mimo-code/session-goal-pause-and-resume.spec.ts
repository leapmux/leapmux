import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { mimoTest } from '../mimo-fixtures'
import { exerciseMiMoShellToolExecution } from './shellToolExecution'

mimoTest('refuses unsupported goal actions on the running native provider', async ({ native }) => {
  await expectUnsupportedGoalActions(native, { actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME], relatedProof: () => exerciseMiMoShellToolExecution(native, { includeFailure: false }) })
})

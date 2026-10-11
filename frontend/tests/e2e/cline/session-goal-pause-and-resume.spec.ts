import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { clineTest } from '../cline-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'

clineTest('refuses unsupported goal actions on the running native provider', async ({ native }) => {
  await expectUnsupportedGoalActions(native, { actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME], relatedProof: () => exerciseShellToolExecution(native, { includeFailure: false }) })
})

import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampTest } from '../amp-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'

ampTest('refuses unsupported goal actions on the running native provider', async ({ native }) => {
  await expectUnsupportedGoalActions(native, { actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME], relatedProof: () => exerciseShellToolExecution(native, { includeFailure: false }) })
})

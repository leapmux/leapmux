import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('refuses unsupported goal actions on the running native provider', async ({ native }) => {
  await expectUnsupportedGoalActions(native, { actions: [AgentGoalAction.SET, AgentGoalAction.CLEAR], relatedProof: () => exerciseShellToolExecution(native, { includeFailure: false }) })
})

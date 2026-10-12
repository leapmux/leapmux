import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { commandCodeTest } from '../command-code-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'

commandCodeTest('refuses the unsupported native goal actions and keeps the Worker state', async ({ native }) => {
  await expectUnsupportedGoalActions(native, { actions: [AgentGoalAction.SET, AgentGoalAction.CLEAR], relatedProof: () => exerciseShellToolExecution(native, { includeFailure: false }) })
})

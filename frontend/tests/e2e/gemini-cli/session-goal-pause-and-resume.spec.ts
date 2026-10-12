import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { geminiTest } from '../gemini-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'

geminiTest('refuses unsupported goal actions on the active native provider', async ({ native }) => {
  await expectUnsupportedGoalActions(native, { actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME], relatedProof: () => exerciseShellToolExecution(native, { includeFailure: false }) })
})

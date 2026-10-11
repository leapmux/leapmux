import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'

claudeTest('refuses unsupported native goal pause and resume actions through the Worker', async ({ native }) => {
  await expectUnsupportedGoalActions(native, {
    actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME],
    relatedProof: () => exerciseShellToolExecution(native),
  })
})

import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { antigravityTest } from '../antigravity-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'

antigravityTest('refuses unsupported goal actions on the active native provider', async ({ native }) => {
  await expectUnsupportedGoalActions(native, {
    actions: [AgentGoalAction.SET, AgentGoalAction.CLEAR],
    relatedProof: () => exerciseShellToolExecution(native, { includeFailure: false }),
  })
})

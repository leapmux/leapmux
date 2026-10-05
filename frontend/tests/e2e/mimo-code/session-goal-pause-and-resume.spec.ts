import { AgentGoalAction, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'
import { exerciseMiMoShellToolExecution } from './shellToolExecution'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest('refuses unsupported goal actions on the running native provider', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await expectUnsupportedGoalActions(context, { actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME], relatedProof: () => exerciseMiMoShellToolExecution(context, { includeFailure: false }) })
})

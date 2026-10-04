import { AgentGoalAction, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'

clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

clineTest('refuses unsupported goal actions on the running native provider', async ({ page, modelScript, leapmuxServer, authenticatedClineWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedClineWorkspace.workspaceId, provider: AgentProvider.CLINE }
  await expectUnsupportedGoalActions(context, { actions: [AgentGoalAction.SET, AgentGoalAction.CLEAR], relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})

import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { commandCodeTest } from '../command-code-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

commandCodeTest('refuses the unsupported native goal actions and keeps the Worker state', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  await expectUnsupportedGoalActions(context, { actions: [AgentGoalAction.SET, AgentGoalAction.CLEAR], relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})

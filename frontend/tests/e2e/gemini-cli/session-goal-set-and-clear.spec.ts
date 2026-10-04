import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GEMINI_E2E_SKIP_REASON, geminiTest } from '../gemini-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest('refuses unsupported goal actions on the active native provider', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await expectUnsupportedGoalActions(context, { actions: [AgentGoalAction.SET, AgentGoalAction.CLEAR], relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { expectNativeCodeExecutionAbsent } from '../helpers/nativeCodeExecution'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'

lettaTest('confirms the native code executor is absent from the actual model catalog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.LETTA }
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('native-code-limit-'), agentOpenOptions(context.provider))
  await openWorkspace(page, context.workspaceId)
  const request = await sendNativeAnswer(context, 'Reply once while the native tool catalog remains available.', 'The actual native catalog turn completed.')
  expectNativeCodeExecutionAbsent(request, ['codemode', 'exec', 'eval', 'REPL', 'js_execution', 'code_execution', 'execute_tools', 'mcp__node_repl__js'])
})

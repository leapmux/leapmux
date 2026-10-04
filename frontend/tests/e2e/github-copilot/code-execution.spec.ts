import { expect } from '@playwright/test'
import { COPILOT_PERMISSION_MODE } from '../../../src/generated/contracts/copilot-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { copilotTest } from '../copilot-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { expectNativeCodeExecutionAbsent } from '../helpers/nativeCodeExecution'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { readCopilotBuiltinCatalog } from './toolCatalog'

copilotTest('confirms the native code executor is absent from the actual model catalog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.GITHUB_COPILOT }
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('native-code-limit-'), { agentProvider: context.provider, ...agentOpenOptions(agentSettings(context.provider)), optionValues: { ...agentOpenOptions(agentSettings(context.provider)).optionValues, permissionMode: COPILOT_PERMISSION_MODE.Manual } })
  await openWorkspace(page, context.workspaceId)
  const request = await sendNativeAnswer(context, 'Reply once while the native tool catalog remains available.', 'The actual native catalog turn completed.')
  const complete = await readCopilotBuiltinCatalog(context)
  expect(complete).toHaveLength(15)
  expect(complete).toContain('bash')
  expect(complete.filter(name => /^(?:exec|eval|repl|codemode|code_execution|js_execution)$/.test(name))).toEqual([])
  expectNativeCodeExecutionAbsent(request, ['codemode', 'exec', 'eval', 'REPL', 'js_execution', 'code_execution', 'execute_tools', 'mcp__node_repl__js'])
})

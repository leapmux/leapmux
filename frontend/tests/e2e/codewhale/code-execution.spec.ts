import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { codewhaleTest } from '../codewhale-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codewhaleToolSearchToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'

codewhaleTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('native-code-execution-'), { agentProvider: context.provider, ...agentOpenOptions(agentSettings(context.provider)) })
  await openWorkspace(page, context.workspaceId)
  await modelScript.queue({ toolCalls: [codewhaleToolSearchToolCall('discover-native-executor', 'execute_tools')] }, { text: 'The native executor schema is loaded.' })
  await sendMessage(page, modelScript.prompt('Discover the native execute_tools schema.'))
  const discovered = await modelScript.waitForSteps(2)
  const schema = nativeToolResult(discovered.requests.find(request => request.stepIndex === 1), 'discover-native-executor')
  expect(JSON.parse(schema)).toEqual({
    type: 'tool_search_tool_search_result',
    tool_references: [{ type: 'tool_reference', tool_name: 'execute_tools' }],
    unavailable_tool_references: [],
  })
  const catalog = discovered.requests.find(request => request.stepIndex === 1)
  if (!catalog)
    throw new Error('The native Codewhale discovery has no following model catalog.')
  nativeCodeExecutionSchema(catalog, 'execute_tools', { code: 'string' })
  await waitForAgentIdle(page)
  await exerciseNativeCodeExecution(context, {
    catalogProof: (request) => {
      nativeCodeExecutionSchema(request, 'execute_tools', { code: 'string' })
    },
    scripts: marker => [
      { label: 'output', source: `return ${JSON.stringify(marker)} + (40 + 2);`, expected: `${marker}42`, failed: false },
      { label: 'failure', source: `throw new Error(${JSON.stringify(marker)} + (70 + 7));`, expected: `${marker}77`, failed: true },
    ],
  })
})

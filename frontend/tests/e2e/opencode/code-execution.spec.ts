import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { readMcpCallArguments } from '../helpers/mcpRequestReceipt'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { withNativeWorker } from '../helpers/nativeWorker'
import { opencodeMcpServerConfiguration } from '../helpers/opencodeMcpLimit'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const directory = createTestDirectory('native-code-mcp-')
  const receiptLog = join(directory, 'native-code-mcp-receipt.json')
  const script = writeMcpResultServer(directory, { receiptLog })
  const original = leapmuxServer.agentEnv.OPENCODE_CONFIG_CONTENT
  if (!original)
    throw new Error('The native executor requires its isolated provider configuration.')
  const content = opencodeMcpServerConfiguration(original, 'result_probe', [process.execPath, script])
  await withNativeWorker(leapmuxServer, { dataDirPrefix: 'native-code-worker', workerName: 'Native code executor', env: { OPENCODE_EXPERIMENTAL_CODE_MODE: 'true', OPENCODE_CONFIG_CONTENT: content } }, async ({ server }) => {
    const context = { page, modelScript, leapmuxServer: server, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.OPENCODE }
    await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, context.workspaceId, directory, { agentProvider: context.provider, ...agentOpenOptions(agentSettings(context.provider)) })
    await openWorkspace(page, context.workspaceId)
    await exerciseNativeCodeExecution(context, {
      catalogProof: (request) => {
        nativeCodeExecutionSchema(request, 'execute', { code: 'string' })
      },
      nativeProof: async (_request, callId) => {
        if (callId !== 'native-code-2' && callId !== 'native-code-3')
          return
        const calls = readMcpCallArguments(receiptLog)
        if (callId === 'native-code-2') {
          expect(calls).toHaveLength(1)
          expect(calls[0]).toEqual({ name: 'inspect', arguments: { count: 0, enabled: false, text: 'native-nested-value' } })
        }
        else {
          expect(calls).toHaveLength(2)
          expect(calls[1]).toEqual({ name: 'fail', arguments: {} })
        }
      },
      scripts: marker => [
        { label: 'output', source: `return ${JSON.stringify(marker)} + (40 + 2);`, expected: `${marker}42`, failed: false },
        { label: 'failure', source: `throw new Error(${JSON.stringify(marker)} + (70 + 7));`, expected: `${marker}77`, failed: true },
        { label: 'nested MCP', source: `const value = await tools.result_probe.inspect({ count: 0, enabled: false, text: "native-nested-value" }); return ${JSON.stringify(marker)} + value.nextCount;`, expected: `${marker}1`, failed: false },
        { label: 'nested MCP failure', source: 'return await tools.result_probe.fail({});', expected: 'NATIVE_MCP_FAILED_RESULT', failed: true },
      ],
    })
  })
})

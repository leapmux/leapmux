import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { withMockPiModel } from '../helpers/scriptedPiModel'
import { getGlobalState } from '../helpers/server'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectUnsupportedMcpInput } from '../helpers/unsupportedMcpInput'
import { piTest } from '../pi-fixtures'
import { writePiMcpConfiguration } from './mcpConfiguration'

piTest('returns the native MCP elicitation refusal without a browser form', async ({ page, modelScript, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const directory = createTestDirectory('pi-native-mcp-form-')
  const receiptLog = join(directory, 'native-form-receipt.json')
  const script = writeMcpFormServer(directory, 'form-server.mjs', { receiptLog })
  writePiMcpConfiguration(directory, getGlobalState().tmpDir, { form_probe: { command: process.execPath, args: [script] } })
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.PI }
    const callId = 'native-pi-elicitation'
    await expectUnsupportedMcpInput(context, { receiptLog, callId, invoke: async () => {
      await modelScript.queue(
        { toolCalls: [mcpToolCall(AgentProvider.PI, callId, { server: 'form_probe', tool: 'ask', input: {} })] },
        { text: 'The native Pi elicitation refusal reached the model.' },
      )
      await sendMessage(page, modelScript.prompt('Call the native MCP form tool once.'))
      const status = await modelScript.waitForSteps(2)
      await waitForAgentIdle(page)
      const request = status.requests.find(record => record.stepIndex === 1)
      if (!request)
        throw new Error('The native Pi elicitation refusal reached no following model request.')
      return request
    } })
  })
})

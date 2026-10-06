import { join } from 'node:path'
import { openAgentViaAPI } from '../helpers/api'
import { invokeNativeMcpTool } from '../helpers/mcpExecution'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { createTestDirectory } from '../helpers/runDirectory'
import { withMockPiModel } from '../helpers/scriptedPiModel'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { expectUnsupportedMcpInput } from '../helpers/unsupportedMcpInput'
import { piTest } from '../pi-fixtures'
import { writePiMcpConfiguration } from './mcpConfiguration'
import { nativeContext } from './scenarios'

piTest('returns the native MCP elicitation refusal without a browser form', async ({ page, modelScript, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const directory = createTestDirectory('pi-native-mcp-form-')
  const receiptLog = join(directory, 'native-form-receipt.json')
  const server = writeMcpFormServer(directory, 'form-server.mjs', { receiptLog })
  writePiMcpConfiguration(directory, getGlobalState().tmpDir, { [server.name]: server })
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, { agentProvider: context.provider, ...settings })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const callId = 'native-pi-elicitation'
    await expectUnsupportedMcpInput(context, { receiptLog, callId, additionalTestIds: ['control-banner'], invoke: () => invokeNativeMcpTool(context, { server: server.name, tool: 'ask', callId, input: {} }) })
  })
})

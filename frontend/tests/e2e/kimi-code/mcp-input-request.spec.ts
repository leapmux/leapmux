import { join } from 'node:path'
import { invokeNativeMcpTool, withNativeMcpFormAgent } from '../helpers/mcpExecution'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { expectUnsupportedMcpInput } from '../helpers/unsupportedMcpInput'
import { kimiTest } from '../kimi-fixtures'
import { nativeContext } from './scenarios'

kimiTest('returns the actual native MCP unsupported-method reply without a browser form', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const configurationPath = join(leapmuxServer.agentEnv.KIMI_CODE_HOME!, 'mcp.json')
  await withNativeMcpFormAgent(context, {
    directoryPrefix: 'kimi-native-mcp-refusal-',
    configurationPath,
    configuration: server => mcpServersConfig(server),
  }, async ({ server, receiptLog }) => {
    await waitForMcpToolListed(receiptLog, 'ask')
    const callId = 'native-kimi-form-refusal'
    await expectUnsupportedMcpInput(context, { receiptLog, callId, additionalTestIds: ['control-banner'], invoke: () => invokeNativeMcpTool(context, { server: server.name, tool: 'ask', callId, input: {} }) })
  })
})

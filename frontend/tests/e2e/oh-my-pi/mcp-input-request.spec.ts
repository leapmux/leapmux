import { join } from 'node:path'
import { invokeNativeMcpTool, withNativeMcpFormAgent } from '../helpers/mcpExecution'
import { waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { expectUnsupportedMcpInput } from '../helpers/unsupportedMcpInput'
import { OH_MY_PI_AGENT, ohMyPiTest } from '../ohmypi-fixtures'
import { nativeContext } from './scenarios'

ohMyPiTest('returns the actual native MCP unsupported-method reply without a browser form', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const configurationPath = join(leapmuxServer.agentEnv.HOME!, '.omp', 'profiles', leapmuxServer.agentEnv.OMP_PROFILE!, 'agent', 'mcp.json')
  await withNativeMcpFormAgent(context, {
    providerAgent: OH_MY_PI_AGENT,
    directoryPrefix: 'ohmypi-native-mcp-refusal-',
    configurationPath,
    configuration: server => ({ mcpServers: { [server.name]: { type: 'stdio', command: server.command, args: server.args } } }),
  }, async ({ server, receiptLog }) => {
    await waitForMcpToolListed(receiptLog, 'ask')
    const callId = 'native-ohmypi-form-refusal'
    await expectUnsupportedMcpInput(context, { receiptLog, callId, additionalTestIds: ['control-banner'], invoke: () => invokeNativeMcpTool(context, { server: server.name, tool: 'ask', callId, input: {} }) })
  })
})

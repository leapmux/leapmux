import { AMP_AGENT, ampTest } from '../amp-fixtures'
import { invokeNativeMcpTool, withNativeMcpFormAgent } from '../helpers/mcpExecution'
import { waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { expectUnsupportedMcpInput } from '../helpers/unsupportedMcpInput'
import { ampMcpSettings, ampSettingsPath } from './mcpConfiguration'
import { nativeContext } from './scenarios'

ampTest('returns the actual native MCP unsupported-method reply without a browser form', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const configurationPath = ampSettingsPath(leapmuxServer.agentEnv)
  await withNativeMcpFormAgent(context, {
    providerAgent: AMP_AGENT,
    directoryPrefix: 'amp-native-mcp-refusal-',
    configurationPath,
    configuration: server => ampMcpSettings(configurationPath, server),
  }, async ({ server, receiptLog }) => {
    // The Worker starts the Amp process for the first message, and Amp starts its MCP servers with that process.
    // So no server can list its tools before a first turn.
    await sendNativeAnswer(context, 'Start the native session that loads the registered form server.', 'The native session started.')
    await waitForMcpToolListed(receiptLog, 'ask')
    const callId = 'native-amp-form-refusal'
    await expectUnsupportedMcpInput(context, { receiptLog, callId, additionalTestIds: ['control-banner'], invoke: () => invokeNativeMcpTool(context, { server: server.name, tool: 'ask', callId, input: {} }) })
  })
})

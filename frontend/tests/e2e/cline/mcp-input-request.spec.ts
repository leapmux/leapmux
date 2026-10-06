import { join } from 'node:path'
import { clineTest } from '../cline-fixtures'
import { invokeNativeMcpTool, withNativeMcpFormAgent } from '../helpers/mcpExecution'
import { waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { expectUnansweredMcpInput } from '../helpers/unsupportedMcpInput'
import { nativeContext } from './scenarios'
import { clineToolError } from './toolError'

/**
 * The request budget of the probe server, in seconds: Cline's own `timeout` field.
 *
 * Cline's stdio MCP client never answers an input request. It reads a message
 * only as the reply to one of its own pending requests, matched by a numeric
 * id, and it drops every other message, a server's own request included
 * (`StdioMcpClient.handleStdout` in Cline's
 * `sdk/packages/core/src/extensions/mcp/client.ts`). Its initialize declares no
 * elicitation capability either. The `tools/call` that waits on the input
 * request therefore ends only when this budget runs out, with Cline's own
 * timeout error. The default budget is 60 seconds. A configured budget also
 * limits the initialize, and 10 seconds is enough for a local Node server.
 */
const MCP_TIMEOUT_SECONDS = 10

clineTest('times out an MCP input request that the native client never answers, without a browser form', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  // Cline reads a failed tool call as the output `{ error: <message> }`, so the context reads the error alone.
  const context = { ...await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId }), readToolResult: clineToolError }
  await withNativeMcpFormAgent(context, {
    directoryPrefix: 'cline-native-mcp-unanswered-',
    configurationPath: join(leapmuxServer.agentEnv.CLINE_DATA_DIR!, 'settings', 'cline_mcp_settings.json'),
    configuration: server => ({ mcpServers: { [server.name]: { transport: { type: 'stdio', command: server.command, args: server.args }, timeout: MCP_TIMEOUT_SECONDS } } }),
  }, async ({ server, receiptLog }) => {
    await waitForMcpToolListed(receiptLog, 'ask')
    const callId = 'native-cline-form-unanswered'
    await expectUnansweredMcpInput(context, {
      receiptLog,
      callId,
      additionalTestIds: ['control-banner'],
      nativeFailureText: `MCP request to "${server.name}" (tools/call) timed out after ${MCP_TIMEOUT_SECONDS}s.`,
      invoke: () => invokeNativeMcpTool(context, { server: server.name, tool: 'ask', callId, input: {} }),
    })
  })
})

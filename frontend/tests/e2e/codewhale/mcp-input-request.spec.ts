import { join } from 'node:path'
import { CODEWHALE_AGENT, codewhaleTest } from '../codewhale-fixtures'
import { invokeNativeMcpTool, withNativeMcpFormAgent } from '../helpers/mcpExecution'
import { waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { expectUnansweredMcpInput } from '../helpers/unsupportedMcpInput'
import { nativeContext } from './scenarios'

/**
 * The time limit of a call to the probe server, in seconds: Codewhale's own
 * `execute_timeout` field.
 *
 * Codewhale's MCP client never answers an input request. Its `initialize`
 * declares no elicitation capability. While it waits for the reply to its own
 * request, it skips every message whose id differs, and a server's own request
 * is such a message (`McpConnection::recv` in Codewhale's `tui/src/mcp.rs`).
 * The `tools/call` that waits on the input request therefore ends only when
 * this limit expires, with Codewhale's own timeout error. The default limit is
 * 60 seconds. The limit applies to tool calls only. `connect_timeout` limits
 * the initialize.
 */
const MCP_EXECUTE_TIMEOUT_SECONDS = 10

codewhaleTest('times out an MCP input request that the native client never answers, without a browser form', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await withNativeMcpFormAgent(context, {
    providerAgent: CODEWHALE_AGENT,
    directoryPrefix: 'codewhale-native-mcp-unanswered-',
    configurationPath: join(leapmuxServer.agentEnv.CODEWHALE_HOME!, 'mcp.json'),
    configuration: server => ({ servers: { [server.name]: { command: server.command, args: server.args, execute_timeout: MCP_EXECUTE_TIMEOUT_SECONDS } } }),
  }, async ({ server, receiptLog }) => {
    const callId = 'native-codewhale-form-unanswered'
    await expectUnansweredMcpInput(context, {
      receiptLog,
      callId,
      additionalTestIds: ['control-banner'],
      nativeFailureText: `MCP tool failed: MCP method 'tools/call' on server '${server.name}' timed out after ${MCP_EXECUTE_TIMEOUT_SECONDS}s`,
      invoke: async () => {
        const request = await invokeNativeMcpTool(context, { server: server.name, tool: 'ask', callId, input: {} })
        // The direct MCP call initializes Codewhale's lazy server pool, so the server lists its tools during the call.
        await waitForMcpToolListed(receiptLog, 'ask')
        return request
      },
    })
  })
})

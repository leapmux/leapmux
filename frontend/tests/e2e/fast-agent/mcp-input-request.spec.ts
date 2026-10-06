import { join } from 'node:path'
import { fastAgentTest } from '../fastagent-fixtures'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { expectCancelledMcpInput } from '../helpers/unsupportedMcpInput'
import { connectNativeMcp, invokeNativeMcp } from './mcpScenarios'

fastAgentTest('returns the actual native MCP input cancel without a browser form', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const receiptLog = join(agent.workingDir, 'form-receipt.json')
  const server = writeMcpFormServer(agent.workingDir, 'form-server.mjs', { receiptLog })
  await connectNativeMcp(native, server, receiptLog)
  const callId = 'fast-mcp-input'
  // Fast Agent declares MCP elicitation and answers through its terminal form.
  // Under ACP, stdin carries the protocol, and that form ends with its default cancel action.
  await expectCancelledMcpInput(native, {
    receiptLog,
    callId,
    invoke: () => invokeNativeMcp(native, { server: server.name, tool: 'ask', input: {}, callId }),
  })
})

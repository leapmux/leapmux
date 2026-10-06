import { join } from 'node:path'
import { geminiTest } from '../gemini-fixtures'
import { invokeNativeMcpTool } from '../helpers/mcpExecution'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { expectUnsupportedMcpInput } from '../helpers/unsupportedMcpInput'
import { withGeminiMcp } from './mcpScenarios'

geminiTest('returns the exact native MCP input refusal without a browser form', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const receiptLog = join(agent.workingDir, 'gemini-form-receipt.json')
  const server = writeMcpFormServer(agent.workingDir, 'gemini-form-server.mjs', { receiptLog })
  await withGeminiMcp(native, server, async () => {
    await expectUnsupportedMcpInput(native, { receiptLog, callId: 'gemini-input-refusal', invoke: () => invokeNativeMcpTool(native, { server: server.name, tool: 'ask', callId: 'gemini-input-refusal', input: {} }) })
  })
})

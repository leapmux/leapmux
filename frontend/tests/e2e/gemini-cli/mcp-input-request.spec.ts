import { join } from 'node:path'
import process from 'node:process'
import { geminiTest } from '../gemini-fixtures'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { expectUnsupportedMcpInput } from '../helpers/unsupportedMcpInput'
import { invokeGeminiMcp, withGeminiMcp } from './mcpScenarios'
import { nativeContext } from './scenarios'

geminiTest('returns the exact native MCP input refusal without a browser form', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  const receiptLog = join(agent.workingDir, 'gemini-form-receipt.json')
  const script = writeMcpFormServer(agent.workingDir, 'gemini-form-server.mjs', { receiptLog })
  await withGeminiMcp(context, { name: 'form_probe', command: process.execPath, args: [script] }, async () => {
    await expectUnsupportedMcpInput(context, { receiptLog, callId: 'gemini-input-refusal', invoke: () => invokeGeminiMcp(context, { server: 'form_probe', tool: 'ask', callId: 'gemini-input-refusal', input: {} }) })
  })
})

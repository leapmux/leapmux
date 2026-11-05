import { join } from 'node:path'
import { antigravityTest } from '../antigravity-fixtures'
import { invokeNativeMcpTool } from '../helpers/mcpExecution'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { expectCancelledMcpInput } from '../helpers/unsupportedMcpInput'
import { withAntigravityMcp } from './mcpScenarios'

antigravityTest('returns the exact native MCP cancellation without a browser form', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const receiptLog = join(agent.workingDir, 'antigravity-form-receipt.json')
  const server = writeMcpFormServer(agent.workingDir, 'antigravity-form-server.mjs', { receiptLog })
  await withAntigravityMcp(native, server, async () => {
    await expectCancelledMcpInput(native, {
      receiptLog,
      callId: 'antigravity-input-refusal',
      invoke: () => invokeNativeMcpTool(native, {
        server: server.name,
        tool: 'ask',
        callId: 'antigravity-input-refusal',
        input: {},
      }),
    })
  })
})

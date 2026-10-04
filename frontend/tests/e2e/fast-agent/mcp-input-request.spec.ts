import { existsSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { fastAgentTest } from '../fastagent-fixtures'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { expectUnsupportedMcpInput } from '../helpers/unsupportedMcpInput'
import { connectNativeMcp, invokeNativeMcp } from './mcpScenarios'
import { nativeContext } from './scenarios'

fastAgentTest('returns the actual native MCP input refusal without a browser form', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  const receiptLog = join(agent.workingDir, 'form-receipt.json')
  const script = writeMcpFormServer(agent.workingDir, 'form-server.mjs', { receiptLog })
  await connectNativeMcp(context, 'form_probe', process.execPath, script)
  await expect.poll(() => existsSync(receiptLog)).toBe(true)
  const callId = 'fast-mcp-input'
  await expectUnsupportedMcpInput(context, {
    receiptLog,
    callId,
    invoke: () => invokeNativeMcp(context, { server: 'form_probe', tool: 'ask', input: {}, callId }),
  })
})

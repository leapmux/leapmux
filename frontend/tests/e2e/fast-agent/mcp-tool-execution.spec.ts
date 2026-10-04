import { randomUUID } from 'node:crypto'
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { fastAgentTest } from '../fastagent-fixtures'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { connectNativeMcp, invokeNativeMcp } from './mcpScenarios'
import { nativeContext } from './scenarios'

fastAgentTest('returns actual local MCP results for a value and an empty string', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  const receiptLog = join(agent.workingDir, 'echo-receipt.json')
  const script = writeMcpEchoServer(agent.workingDir, { receiptLog })
  await connectNativeMcp(context, 'echo_probe', process.execPath, script)
  await expect.poll(() => existsSync(receiptLog)).toBe(true)
  for (const value of [`ECHOVALUE${randomUUID()}`, '']) {
    const callId = `fast-echo-${randomUUID()}`
    const next = await invokeNativeMcp(context, { server: 'echo_probe', tool: 'echo', input: { value }, callId })
    const expected = `MCP_ECHO:${value}`
    expect(nativeToolResult(next, callId)).toContain(expected)
    const receipt = readMcpServerReceipt(receiptLog)
    expect(receipt.toolResults.at(-1)).toMatchObject({ tool: 'echo', text: expected, isError: false })
    expect(receipt.toolCatalogs.flatMap(catalog => catalog.tools.map(tool => tool.name))).toContain('echo')
  }
})

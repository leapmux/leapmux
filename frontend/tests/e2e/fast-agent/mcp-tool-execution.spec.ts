import { randomUUID } from 'node:crypto'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { fastAgentTest } from '../fastagent-fixtures'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { mcpReceiptListsTool, readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { connectNativeMcp, invokeNativeMcp } from './mcpScenarios'

fastAgentTest('returns actual local MCP results for a value and an empty string', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const receiptLog = join(agent.workingDir, 'echo-receipt.json')
  const server = writeMcpEchoServer(agent.workingDir, { receiptLog })
  await connectNativeMcp(native, server, receiptLog)
  for (const value of [`ECHOVALUE${randomUUID()}`, '']) {
    const callId = `fast-echo-${randomUUID()}`
    const next = await invokeNativeMcp(native, { server: server.name, tool: 'echo', input: { value }, callId })
    const expected = `MCP_ECHO:${value}`
    expect(nativeToolResult(next, callId)).toContain(expected)
    const receipt = readMcpServerReceipt(receiptLog)
    expect(receipt.toolResults.at(-1)).toMatchObject({ tool: 'echo', text: expected, isError: false })
    expect(mcpReceiptListsTool(receipt, 'echo')).toBe(true)
  }
})

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { antigravityTest } from '../antigravity-fixtures'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { exerciseMcpEcho, invokeNativeMcpTool } from '../helpers/mcpExecution'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { readMcpCallArguments } from '../helpers/mcpServerReceipt'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { toolCallRow } from '../helpers/ui'
import { withAntigravityMcp } from './mcpScenarios'
import { antigravityNativeToolResultContent } from './toolResult'

antigravityTest('executes a real MCP echo through the native Google model protocol', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const receiptLog = join(agent.workingDir, 'antigravity-mcp-echo-receipt.json')
  const server = writeMcpEchoServer(agent.workingDir, { receiptLog })
  await withAntigravityMcp(native, server, async () => {
    await exerciseMcpEcho(native, 'antigravity', { receiptLog })
  })
})

antigravityTest('preserves native MCP zero and empty arguments and failed results after reload', async ({ native: context, page }) => {
  const parent = await currentNativeAgent(context)
  const receiptLog = join(parent.workingDir, 'antigravity-mcp-result-receipt.json')
  const server = writeMcpResultServer(parent.workingDir, { receiptLog })
  await withAntigravityMcp(context, server, async () => {
    const operations = [
      {
        tool: 'inspect',
        callId: 'antigravity-mcp-inspect',
        input: { count: 0, enabled: false, text: '' },
        expected: 'NATIVE_MCP_INSPECT:{"count":0,"enabled":false,"text":""}',
        failed: false,
      },
      {
        tool: 'fail',
        callId: 'antigravity-mcp-failure',
        input: {},
        expected: 'NATIVE_MCP_FAILED_RESULT',
        failed: true,
      },
    ]
    for (const operation of operations) {
      const call = {
        server: server.name,
        tool: operation.tool,
        callId: operation.callId,
        input: operation.input,
      }
      const request = await invokeNativeMcpTool(context, call)
      expect(request.protocol).toBe('google-generative-language')
      const response = antigravityNativeToolResultContent(request, operation.callId)
      if (!isObject(response))
        throw new Error('The native Antigravity MCP response must be an object.')
      const field = operation.failed ? 'error' : 'output'
      const returned = response[field]
      if (typeof returned !== 'string')
        throw new TypeError(`The native Antigravity MCP response has no ${field} string.`)
      expect(response).not.toHaveProperty(operation.failed ? 'output' : 'error')
      expect(returned).toContain(operation.expected)
      if (!operation.failed) {
        const serialized = JSON.stringify(response)
        expect(serialized).not.toContain('privateFixture')
        expect(serialized).not.toContain('nextCount')
      }
      const result = toolCallRow(page, operation.callId)
      await expect(result).toHaveAttribute('data-tool-status', operation.failed ? 'failed' : 'completed')
      await expect(result).toContainText(operation.expected)
      await page.reload()
      await expect(result).toHaveAttribute('data-tool-status', operation.failed ? 'failed' : 'completed')
      await expect(result).toContainText(operation.expected)
    }
    expect(readMcpCallArguments(receiptLog)).toEqual([
      {
        name: 'inspect',
        arguments: { count: 0, enabled: false, text: '' },
      },
      { name: 'fail', arguments: {} },
    ])
  })
})

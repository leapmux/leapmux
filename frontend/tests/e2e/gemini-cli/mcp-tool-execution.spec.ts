import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { geminiTest } from '../gemini-fixtures'
import { exerciseMcpEcho, invokeNativeMcpTool } from '../helpers/mcpExecution'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { readMcpCallArguments } from '../helpers/mcpServerReceipt'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResultContent } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { toolCallRow } from '../helpers/ui'
import { withGeminiMcp } from './mcpScenarios'
import { readGeminiStoredToolRecord } from './toolRecord'

geminiTest('executes a real MCP echo through the native Google model protocol', async ({ native }) => {
  await exerciseMcpEcho(native, 'gemini')
})

geminiTest('preserves native MCP zero and empty arguments and failed results after reload', async ({ native: context, page }) => {
  const parent = await currentNativeAgent(context)
  const receiptLog = join(parent.workingDir, 'gemini-mcp-result-receipt.json')
  const server = writeMcpResultServer(parent.workingDir, { receiptLog })
  await withGeminiMcp(context, server, async () => {
    const operations = [
      { tool: 'inspect', callId: 'gemini-mcp-inspect', input: { count: 0, enabled: false, text: '' }, expected: 'NATIVE_MCP_INSPECT:{"count":0,"enabled":false,"text":""}', failed: false },
      { tool: 'fail', callId: 'gemini-mcp-failure', input: {}, expected: 'NATIVE_MCP_FAILED_RESULT', failed: true },
    ]
    for (const operation of operations) {
      const call = { server: server.name, tool: operation.tool, callId: operation.callId, input: operation.input }
      const request = await invokeNativeMcpTool(context, call)
      expect(request.protocol).toBe('google-generative-language')
      const response = nativeToolResultContent(request, operation.callId)
      if (!isObject(response))
        throw new Error('The native Gemini MCP response must be an object.')
      const field = operation.failed ? 'error' : 'output'
      const returned = response[field]
      if (typeof returned !== 'string')
        throw new Error(`The native Gemini MCP response has no ${field} string.`)
      expect(response).not.toHaveProperty(operation.failed ? 'output' : 'error')
      expect(returned).toContain(operation.expected)
      if (!operation.failed) {
        const serialized = JSON.stringify(response)
        expect(serialized).not.toContain('privateFixture')
        expect(serialized).not.toContain('nextCount')
      }
      // Gemini stores each call under its tool name and the call ID of the model.
      const nativeCallId = `${mcpToolCall(context.provider, call.callId, call).name}__${call.callId}`
      const snapshot = await readNativeMessageSnapshot(context, (await currentNativeAgent(context)).id)
      const records = snapshot.messages.filter(row => row.spanId === nativeCallId).map(readGeminiStoredToolRecord).filter(isObject)
      expect(records).toHaveLength(1)
      expect(records[0]).toMatchObject({ id: nativeCallId, status: operation.failed ? 'error' : 'success', args: operation.input })
      if (!operation.failed) {
        expect(JSON.stringify(records[0])).not.toContain('privateFixture')
        expect(JSON.stringify(records[0])).not.toContain('nextCount')
      }
      const result = toolCallRow(page, nativeCallId)
      await expect(result).toHaveAttribute('data-tool-status', operation.failed ? 'failed' : 'completed')
      await expect(result).toContainText(operation.expected)
      await page.reload()
      await expect(result).toHaveAttribute('data-tool-status', operation.failed ? 'failed' : 'completed')
      await expect(result).toContainText(operation.expected)
    }
    expect(readMcpCallArguments(receiptLog)).toEqual([
      { name: 'inspect', arguments: { count: 0, enabled: false, text: '' } },
      { name: 'fail', arguments: {} },
    ])
  })
})

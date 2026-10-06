import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { geminiTest } from '../gemini-fixtures'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResultContent } from '../helpers/nativeToolResult'
import { invokeGeminiMcp, withGeminiMcp } from './mcpScenarios'
import { nativeContext } from './scenarios'
import { readGeminiStoredToolRecord } from './toolRecord'

geminiTest('executes a real MCP echo through the native Google model protocol', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseMcpEcho(context.page, context.modelScript, context.provider, 'gemini')
})

geminiTest('preserves native MCP zero and empty arguments and failed results after reload', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const parent = await currentNativeAgent(context)
  const receiptLog = join(parent.workingDir, 'gemini-mcp-result-receipt.json')
  const script = writeMcpResultServer(parent.workingDir, { receiptLog })
  await withGeminiMcp(context, { name: 'result_probe', command: process.execPath, args: [script] }, async () => {
    const operations = [
      { tool: 'inspect', callId: 'gemini-mcp-inspect', input: { count: 0, enabled: false, text: '' }, expected: 'NATIVE_MCP_INSPECT:{"count":0,"enabled":false,"text":""}', failed: false },
      { tool: 'fail', callId: 'gemini-mcp-failure', input: {}, expected: 'NATIVE_MCP_FAILED_RESULT', failed: true },
    ]
    for (const operation of operations) {
      const request = await invokeGeminiMcp(context, { server: 'result_probe', tool: operation.tool, callId: operation.callId, input: operation.input })
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
      const nativeCallId = `mcp_result_probe_${operation.tool}__${operation.callId}`
      const snapshot = await readNativeMessageSnapshot(context, (await currentNativeAgent(context)).id)
      const records = snapshot.messages.filter(row => row.spanId === nativeCallId).map(readGeminiStoredToolRecord).filter(isObject)
      expect(records).toHaveLength(1)
      expect(records[0]).toMatchObject({ id: nativeCallId, status: operation.failed ? 'error' : 'success', args: operation.input })
      if (!operation.failed) {
        expect(JSON.stringify(records[0])).not.toContain('privateFixture')
        expect(JSON.stringify(records[0])).not.toContain('nextCount')
      }
      const result = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${nativeCallId}"][data-tool-row-role="result"]:visible`)
      await expect(result).toHaveAttribute('data-tool-status', operation.failed ? 'failed' : 'completed')
      await expect(result).toContainText(operation.expected)
      await page.reload()
      await expect(result).toHaveAttribute('data-tool-status', operation.failed ? 'failed' : 'completed')
      await expect(result).toContainText(operation.expected)
    }
    const receipts: unknown = JSON.parse(readFileSync(receiptLog, 'utf8'))
    if (!Array.isArray(receipts))
      throw new Error('The native MCP result server produced no receipt array.')
    const calls = receipts.filter(isObject).map(row => row.request).filter(isObject).filter(row => row.method === 'tools/call')
    expect(calls.map((row) => {
      const params = isObject(row.params) ? row.params : undefined
      return { name: params?.name, arguments: params?.arguments }
    })).toEqual([
      { name: 'inspect', arguments: { count: 0, enabled: false, text: '' } },
      { name: 'fail', arguments: {} },
    ])
  })
})

import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { test } from '../fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { readMcpCallExchange } from '../helpers/mcpRequestReceipt'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall, piCodemodeToolCall, piMcpResourceToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { withMockPiModel } from '../helpers/scriptedPiModel'
import { getGlobalState } from '../helpers/server'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { activateNativeCodemode } from './codemodeConfiguration'
import { writePiMcpConfiguration } from './mcpConfiguration'
import { readPiMcpResult } from './mcpResult'
import { verifyPiOutputFilePaths } from './outputFilePaths'

test('keeps the native real MCP output path and preview after reload', async ({ page, context, modelScript, authenticatedEmptyWorkspace, leapmuxServer }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const provider = AgentProvider.PI
  const directory = createTestDirectory('renderer-pi-full-output-')
  const outputFile = `${Array.from({ length: 3000 }, (_, index) => `full-output-line-${index}`).join('\n')}\nPI_OUTPUT_FILE_RECOVERED`
  const receiptLog = join(directory, 'full-output-receipt.json')
  const resultServer = writeMcpResultServer(directory, { receiptLog })
  writePiMcpConfiguration(directory, getGlobalState().tmpDir, { result_probe: { command: process.execPath, args: [resultServer] } })
  const code = `// @options: {"max_output_tokens": 100}\nconst result = await tools.mcp__result_probe__inspect({count: 0, enabled: false, text: ${JSON.stringify(outputFile)}}); text(result.structuredContent.text);`
  activateNativeCodemode(directory, getGlobalState().tmpDir)
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, { agentProvider: provider, ...settings })
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await modelScript.queue(
      { toolCalls: [piCodemodeToolCall('full-output-call', code)] },
      { text: 'Protocol test complete.' },
    )
    await sendMessage(page, modelScript.prompt('Run the native MCP output path check.'))
    await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    const nativeContext = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider }
    const nested = await readPiMcpResult(nativeContext, 'full-output-call/1', 'mcp__result_probe__inspect')
    expect(nested.failed).toBe(false)
    expect(nested.result.structuredContent).toEqual({ content: [{ type: 'text', text: `NATIVE_MCP_INSPECT:${JSON.stringify({ count: 0, enabled: false, text: outputFile })}` }], structuredContent: { nextCount: 1, enabled: false, text: outputFile } })
    const exchange = readMcpCallExchange(receiptLog)
    expect(exchange.name).toBe('inspect')
    expect(exchange.arguments).toEqual({ count: 0, enabled: false, text: outputFile })
    expect(exchange.result).toMatchObject({ structuredContent: { nextCount: 1, enabled: false, text: outputFile } })
    const completed = await readPiMcpResult(nativeContext, 'full-output-call', 'codemode')
    expect(isObject(completed.result.details) && completed.result.details.fullOutputPath).toMatch(/pi-codemode-[0-9a-f]{16}\.txt$/)
    const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
    const output = page.locator('[data-testid="message-bubble"][data-tool-call-id="full-output-call"][data-tool-row-role="result"]:visible')
    await expect(output).toHaveCount(1)
    await expect(output).toContainText('full-output-line-2999')
    await expect(output).toContainText('PI_OUTPUT_FILE_RECOVERED')
    await expect(output).toContainText('Warning: truncated output')
    await expect(chat.getByText('Protocol test complete.', { exact: true })).toBeVisible()
    await verifyPiOutputFilePaths(nativeContext, { callId: 'full-output-call', expectedText: outputFile, omittedMarker: 'full-output-line-1500' })
    await expect(output).toContainText('full-output-line-2999')
    await expect(output).toContainText('PI_OUTPUT_FILE_RECOVERED')
  })
})

test('uses complete native MCP structured results, failed results, and resources', async ({ page, modelScript, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const directory = createTestDirectory('pi-native-mcp-results-')
  const receiptLog = join(directory, 'native-result-receipt.json')
  const script = writeMcpResultServer(directory, { receiptLog })
  writePiMcpConfiguration(directory, getGlobalState().tmpDir, { result_probe: { command: process.execPath, args: [script] } })
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const nativeContext = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.PI }
    const calls = [
      { call: mcpToolCall(AgentProvider.PI, 'native-inspect', { server: 'result_probe', tool: 'inspect', input: { count: 0, enabled: false, text: '' } }), expected: 'NATIVE_MCP_INSPECT:{"count":0,"enabled":false,"text":""}' },
      { call: mcpToolCall(AgentProvider.PI, 'native-failure', { server: 'result_probe', tool: 'fail', input: {} }), expected: 'NATIVE_MCP_FAILED_RESULT' },
      { call: piMcpResourceToolCall('native-resources', { operation: 'list', server: 'result_probe' }), expected: 'probe://text' },
      { call: piMcpResourceToolCall('native-templates', { operation: 'templates', server: 'result_probe' }), expected: 'probe://template/{id}' },
      { call: piMcpResourceToolCall('native-resource-text', { operation: 'read', server: 'result_probe', uri: 'probe://text' }), expected: 'NATIVE_MCP_RESOURCE_TEXT' },
    ]
    for (const { call, expected } of calls) {
      const start = (await modelScript.status()).stepCount
      await modelScript.queue({ toolCalls: [call] }, { text: `The native ${call.id} result reached the model.` })
      await sendMessage(page, modelScript.prompt(`Run the native ${call.id} probe once.`))
      const status = await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      expect(nativeToolResult(status.requests.find(record => record.stepIndex === start + 1), call.id)).toContain(expected)
      const native = await readPiMcpResult(nativeContext, call.id, call.name)
      expect(native.failed).toBe(call.id === 'native-failure')
      const bubble = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${call.id}"][data-tool-row-role="result"]:visible`)
      await expect(bubble).toHaveAttribute('data-tool-status', call.id === 'native-failure' ? 'failed' : 'completed')
      if (call.id === 'native-inspect') {
        expect(native.result.structuredContent).toEqual({ content: [{ type: 'text', text: expected }], structuredContent: { nextCount: 1, enabled: false, text: '' } })
        const structured = bubble.getByText('Structured', { exact: true }).locator('..').locator('div').filter({ hasText: 'nextCount' }).last()
        await expect(structured).toBeVisible()
        expect(JSON.parse((await structured.textContent()) ?? '')).toEqual({ nextCount: 1, enabled: false, text: '' })
        await page.reload()
        await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
        await expect(structured).toBeVisible()
        expect(JSON.parse((await structured.textContent()) ?? '')).toEqual({ nextCount: 1, enabled: false, text: '' })
      }
      await expect(messageBubbles(page).filter({ hasText: expected }).first()).toBeVisible()
    }
    const receipts: unknown = JSON.parse(readFileSync(receiptLog, 'utf8'))
    expect(Array.isArray(receipts) && receipts.some(record => isObject(record) && isObject(record.request) && isObject(record.request.params) && record.request.params.name === 'inspect' && JSON.stringify(record.request.params.arguments) === JSON.stringify({ count: 0, enabled: false, text: '' }))).toBe(true)
    const native = await readPiMcpResult(nativeContext, 'native-inspect', 'mcp__result_probe__inspect')
    expect(native.result.structuredContent).toEqual({ content: [{ type: 'text', text: calls[0]!.expected }], structuredContent: { nextCount: 1, enabled: false, text: '' } })
  })
})

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { readMcpCallArguments, readMcpCallExchange } from '../helpers/mcpServerReceipt'
import { nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall, piCodemodeToolCall, piMcpResourceToolCall } from '../helpers/providerToolCalls'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { getGlobalState } from '../helpers/server'
import { chatScrollContainer, messageBubbles, openWorkspace, sendMessage, toolCallRow, waitForAgentIdle } from '../helpers/ui'
import { piTest } from '../pi-fixtures'
import { activateNativeCodemode } from './codemodeConfiguration'
import { writePiMcpConfiguration } from './mcpConfiguration'
import { readPiMcpResult } from './mcpResult'
import { verifyPiOutputFilePaths } from './outputFilePaths'
import { nativeContext, PI_AGENT } from './scenarios'
import { withMockPiModel } from './scriptedModel'

piTest('keeps the native real MCP output path and preview after reload', async ({ page, modelScript, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const directory = newProviderWorkingDir(PI_AGENT, 'renderer-pi-full-output-')
  const outputFile = `${Array.from({ length: 3000 }, (_, index) => `full-output-line-${index}`).join('\n')}\nPI_OUTPUT_FILE_RECOVERED`
  const receiptLog = join(directory, 'full-output-receipt.json')
  const resultServer = writeMcpResultServer(directory, { receiptLog })
  writePiMcpConfiguration(directory, getGlobalState().tmpDir, { [resultServer.name]: resultServer })
  // The codemode binding of an MCP tool has the name of the native MCP tool.
  const inspectTool = mcpToolCall(native.provider, 'inspect-name', { server: resultServer.name, tool: 'inspect', input: {} }).name
  const code = `// @options: {"max_output_tokens": 100}\nconst result = await tools.${inspectTool}({count: 0, enabled: false, text: ${JSON.stringify(outputFile)}}); text(result.structuredContent.text);`
  activateNativeCodemode(directory, getGlobalState().tmpDir)
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    await openAgentViaAPI(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, directory, { agentProvider: native.provider, ...settings })
    await page.reload()
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const start = await modelScript.queue(
      { toolCalls: [piCodemodeToolCall('full-output-call', code)] },
      nativeTextStep(native, 'Protocol test complete.'),
    )
    await sendMessage(page, modelScript.prompt('Run the native MCP output path check.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    const nested = await readPiMcpResult(native, 'full-output-call/1', inspectTool)
    expect(nested.failed).toBe(false)
    expect(nested.result.structuredContent).toEqual({ content: [{ type: 'text', text: `NATIVE_MCP_INSPECT:${JSON.stringify({ count: 0, enabled: false, text: outputFile })}` }], structuredContent: { nextCount: 1, enabled: false, text: outputFile } })
    const exchange = readMcpCallExchange(receiptLog)
    expect(exchange.name).toBe('inspect')
    expect(exchange.arguments).toEqual({ count: 0, enabled: false, text: outputFile })
    expect(exchange.result).toMatchObject({ structuredContent: { nextCount: 1, enabled: false, text: outputFile } })
    const completed = await readPiMcpResult(native, 'full-output-call', 'codemode')
    expect(isObject(completed.result.details) && completed.result.details.fullOutputPath).toMatch(/pi-codemode-[0-9a-f]{16}\.txt$/)
    const chat = chatScrollContainer(page)
    const output = toolCallRow(page, 'full-output-call')
    await expect(output).toHaveCount(1)
    await expect(output).toContainText('full-output-line-2999')
    await expect(output).toContainText('PI_OUTPUT_FILE_RECOVERED')
    await expect(output).toContainText('Warning: truncated output')
    await expect(chat.getByText('Protocol test complete.', { exact: true })).toBeVisible()
    await verifyPiOutputFilePaths(native, { callId: 'full-output-call', expectedText: outputFile, omittedMarker: 'full-output-line-1500' })
    await expect(output).toContainText('full-output-line-2999')
    await expect(output).toContainText('PI_OUTPUT_FILE_RECOVERED')
  })
})

piTest('uses complete native MCP structured results, failed results, and resources', async ({ page, modelScript, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const directory = newProviderWorkingDir(PI_AGENT, 'pi-native-mcp-results-')
  const receiptLog = join(directory, 'native-result-receipt.json')
  const server = writeMcpResultServer(directory, { receiptLog })
  writePiMcpConfiguration(directory, getGlobalState().tmpDir, { [server.name]: server })
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openAgentViaAPI(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, directory, { agentProvider: context.provider, ...settings })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const inspect = mcpToolCall(context.provider, 'native-inspect', { server: server.name, tool: 'inspect', input: { count: 0, enabled: false, text: '' } })
    const calls = [
      { call: inspect, expected: 'NATIVE_MCP_INSPECT:{"count":0,"enabled":false,"text":""}' },
      { call: mcpToolCall(context.provider, 'native-failure', { server: server.name, tool: 'fail', input: {} }), expected: 'NATIVE_MCP_FAILED_RESULT' },
      { call: piMcpResourceToolCall('native-resources', { operation: 'list', server: server.name }), expected: 'probe://text' },
      { call: piMcpResourceToolCall('native-templates', { operation: 'templates', server: server.name }), expected: 'probe://template/{id}' },
      { call: piMcpResourceToolCall('native-resource-text', { operation: 'read', server: server.name, uri: 'probe://text' }), expected: 'NATIVE_MCP_RESOURCE_TEXT' },
    ]
    for (const { call, expected } of calls) {
      const start = await modelScript.queue({ toolCalls: [call] }, nativeTextStep(context, `The native ${call.id} result reached the model.`))
      await sendMessage(page, modelScript.prompt(`Run the native ${call.id} probe once.`))
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      expect(nativeToolResult(await modelScript.requestAt(start + 1), call.id)).toContain(expected)
      const native = await readPiMcpResult(context, call.id, call.name)
      expect(native.failed).toBe(call.id === 'native-failure')
      const bubble = toolCallRow(page, call.id)
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
    // A resource operation is no tool call, so the server received exactly the two tool calls.
    expect(readMcpCallArguments(receiptLog)).toEqual([
      { name: 'inspect', arguments: { count: 0, enabled: false, text: '' } },
      { name: 'fail', arguments: {} },
    ])
    const native = await readPiMcpResult(context, inspect.id, inspect.name)
    expect(native.result.structuredContent).toEqual({ content: [{ type: 'text', text: calls[0]!.expected }], structuredContent: { nextCount: 1, enabled: false, text: '' } })
  })
})

import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { test } from '../fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { readMcpCallExchange } from '../helpers/mcpRequestReceipt'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { piCodemodeToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { withMockPiModel } from '../helpers/scriptedPiModel'
import { getGlobalState } from '../helpers/server'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { activateNativeCodemode } from './codemodeConfiguration'
import { writePiMcpConfiguration } from './mcpConfiguration'
import { readPiMcpResult } from './mcpResult'
import { verifyPiOutputFilePaths } from './outputFilePaths'

test('keeps the native codemode output path and exact preview after reload', async ({ page, context, modelScript, authenticatedEmptyWorkspace, leapmuxServer }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const directory = createTestDirectory('pi-output-path-feature-codemode-')
  const output = computedNativeToolOutput()
  activateNativeCodemode(directory, getGlobalState().tmpDir)
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    const workspaceId = authenticatedEmptyWorkspace.workspaceId
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
    await openWorkspace(page, workspaceId)
    const code = `// @options: {"max_output_tokens": 100}\n${output.source}\ntext(completeOutput);`
    expect(code).not.toContain(output.text)
    expect(code).not.toContain(output.omittedMarker)
    const call = piCodemodeToolCall('native-output-path-codemode-only', code)
    await modelScript.queue({ toolCalls: [call] }, { text: 'The computed native output file completed.' })
    await sendMessage(page, modelScript.prompt('Compute the native large output.'))
    const status = await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    const next = status.requests.find(request => request.stepIndex === 1)
    expect(nativeToolResult(next, call.id)).not.toContain(output.omittedMarker)
    const nativeContext = { page, modelScript, leapmuxServer, workspaceId, provider: AgentProvider.PI }
    const completed = await readPiMcpResult(nativeContext, call.id, 'codemode')
    expect(isObject(completed.result.details) && completed.result.details.calls).toEqual([])
    await verifyPiOutputFilePaths(nativeContext, { callId: call.id, expectedText: output.text, omittedMarker: output.omittedMarker })
  })
})

test('keeps the native real MCP output path and exact preview after reload', async ({ page, context, modelScript, authenticatedEmptyWorkspace, leapmuxServer }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const directory = createTestDirectory('pi-output-path-feature-mcp-')
  const receiptLog = join(directory, 'native-output-path-mcp-receipt.json')
  const server = writeMcpResultServer(directory, { receiptLog })
  writePiMcpConfiguration(directory, getGlobalState().tmpDir, { result_probe: { command: process.execPath, args: [server] } })
  activateNativeCodemode(directory, getGlobalState().tmpDir)
  const output = computedNativeToolOutput()
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    const workspaceId = authenticatedEmptyWorkspace.workspaceId
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
    await openWorkspace(page, workspaceId)
    const code = `// @options: {"max_output_tokens": 100}\n${output.source}\nconst result = await tools.mcp__result_probe__inspect({count:0,enabled:false,text:completeOutput});\ntext(result.structuredContent.text);`
    expect(code).not.toContain(output.text)
    expect(code).not.toContain(output.omittedMarker)
    const call = piCodemodeToolCall('native-output-path-real-mcp', code)
    await modelScript.queue({ toolCalls: [call] }, { text: 'The real native MCP output file completed.' })
    await sendMessage(page, modelScript.prompt('Compute the large native output, then invoke the real MCP inspect tool.'))
    const status = await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    expect(nativeToolResult(status.requests.find(request => request.stepIndex === 1), call.id)).not.toContain(output.omittedMarker)
    const nativeContext = { page, modelScript, leapmuxServer, workspaceId, provider: AgentProvider.PI }
    const nested = await readPiMcpResult(nativeContext, `${call.id}/1`, 'mcp__result_probe__inspect')
    expect(nested.failed).toBe(false)
    expect(nested.result.structuredContent).toMatchObject({ structuredContent: { nextCount: 1, enabled: false, text: output.text } })
    const exchange = readMcpCallExchange(receiptLog)
    expect(exchange.name).toBe('inspect')
    expect(exchange.arguments).toEqual({ count: 0, enabled: false, text: output.text })
    expect(exchange.result).toMatchObject({ structuredContent: { nextCount: 1, enabled: false, text: output.text } })
    await verifyPiOutputFilePaths(nativeContext, { callId: call.id, expectedText: output.text, omittedMarker: output.omittedMarker })
  })
})

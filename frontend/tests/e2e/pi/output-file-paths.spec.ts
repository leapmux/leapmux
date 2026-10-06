import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { openAgentViaAPI } from '../helpers/api'
import { readMcpCallExchange } from '../helpers/mcpRequestReceipt'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { piCodemodeToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { withMockPiModel } from '../helpers/scriptedPiModel'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { piTest } from '../pi-fixtures'
import { activateNativeCodemode } from './codemodeConfiguration'
import { writePiMcpConfiguration } from './mcpConfiguration'
import { readPiMcpResult } from './mcpResult'
import { verifyPiOutputFilePaths } from './outputFilePaths'
import { nativeContext } from './scenarios'

piTest('keeps the native codemode output path and exact preview after reload', async ({ page, modelScript, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const directory = createTestDirectory('pi-output-path-feature-codemode-')
  const output = computedNativeToolOutput()
  activateNativeCodemode(directory, getGlobalState().tmpDir)
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
    await openWorkspace(page, context.workspaceId)
    const code = `// @options: {"max_output_tokens": 100}\n${output.source}\ntext(completeOutput);`
    expect(code).not.toContain(output.text)
    expect(code).not.toContain(output.omittedMarker)
    const call = piCodemodeToolCall('native-output-path-codemode-only', code)
    const { resultRequest } = await runNativeToolTurn(context, {
      toolCalls: [call],
      prompt: 'Compute the native large output.',
      answer: 'The computed native output file completed.',
    })
    expect(nativeToolResult(resultRequest, call.id)).not.toContain(output.omittedMarker)
    const completed = await readPiMcpResult(context, call.id, 'codemode')
    expect(isObject(completed.result.details) && completed.result.details.calls).toEqual([])
    await verifyPiOutputFilePaths(context, { callId: call.id, expectedText: output.text, omittedMarker: output.omittedMarker })
  })
})

piTest('keeps the native real MCP output path and exact preview after reload', async ({ page, modelScript, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const directory = createTestDirectory('pi-output-path-feature-mcp-')
  const receiptLog = join(directory, 'native-output-path-mcp-receipt.json')
  const server = writeMcpResultServer(directory, { receiptLog })
  writePiMcpConfiguration(directory, getGlobalState().tmpDir, { result_probe: { command: process.execPath, args: [server] } })
  activateNativeCodemode(directory, getGlobalState().tmpDir)
  const output = computedNativeToolOutput()
  await withMockPiModel(directory, leapmuxServer, async (settings) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
    await openWorkspace(page, context.workspaceId)
    const code = `// @options: {"max_output_tokens": 100}\n${output.source}\nconst result = await tools.mcp__result_probe__inspect({count:0,enabled:false,text:completeOutput});\ntext(result.structuredContent.text);`
    expect(code).not.toContain(output.text)
    expect(code).not.toContain(output.omittedMarker)
    const call = piCodemodeToolCall('native-output-path-real-mcp', code)
    const { resultRequest } = await runNativeToolTurn(context, {
      toolCalls: [call],
      prompt: 'Compute the large native output, then invoke the real MCP inspect tool.',
      answer: 'The real native MCP output file completed.',
    })
    expect(nativeToolResult(resultRequest, call.id)).not.toContain(output.omittedMarker)
    const nested = await readPiMcpResult(context, `${call.id}/1`, 'mcp__result_probe__inspect')
    expect(nested.failed).toBe(false)
    expect(nested.result.structuredContent).toMatchObject({ structuredContent: { nextCount: 1, enabled: false, text: output.text } })
    const exchange = readMcpCallExchange(receiptLog)
    expect(exchange.name).toBe('inspect')
    expect(exchange.arguments).toEqual({ count: 0, enabled: false, text: output.text })
    expect(exchange.result).toMatchObject({ structuredContent: { nextCount: 1, enabled: false, text: output.text } })
    await verifyPiOutputFilePaths(context, { callId: call.id, expectedText: output.text, omittedMarker: output.omittedMarker })
  })
})

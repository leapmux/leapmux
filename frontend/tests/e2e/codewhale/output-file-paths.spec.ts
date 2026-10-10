import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { codewhaleTest } from '../codewhale-fixtures'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { readMcpCallExchange } from '../helpers/mcpServerReceipt'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { codewhaleBashModelMatches } from './bashModelOutput'
import { codewhaleMcpToolResult } from './mcpToolResult'
import { readCodewhaleNativeOutput } from './outputFilePaths'
import { CODEWHALE_AGENT, nativeContext } from './scenarios'
import { codewhaleNativeOutputCallId } from './toolCallIdentity'

codewhaleTest('keeps the native MCP output path and exact preview after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const directory = newProviderWorkingDir(CODEWHALE_AGENT, 'codewhale-native-mcp-output-path-')
  const generated = codewhaleMcpToolResult(computedNativeToolOutput({ lineCount: 8000, padding: 30 }), { approvedByUser: true })
  const receiptLog = join(directory, 'native-mcp-result-receipts.json')
  const server = writeMcpResultServer(directory, { receiptLog, inspectContent: generated.nativeResult.content })
  const home = leapmuxServer.agentEnv.CODEWHALE_HOME
  if (!home)
    throw new Error('The native Codewhale MCP case requires its private home.')
  await withNativeConfigurationFile({ path: join(home, 'mcp.json'), content: JSON.stringify({ servers: { [server.name]: { command: server.command, args: server.args } } }), runDir: getGlobalState().tmpDir }, async () => {
    await openProviderAgent(leapmuxServer, context.workspaceId, CODEWHALE_AGENT, { workingDir: directory })
    await openWorkspace(page, context.workspaceId)
    await captureNativeToolOutput(context, testInfo, {
      output: generated.capture,
      callId: 'native-output-path',
      call: (_output, callId) => mcpToolCall(context.provider, callId, { server: server.name, tool: 'inspect', input: { count: -1, enabled: false, text: '' } }),
      nativeCallId: (_request, scriptedId, snapshot) => codewhaleNativeOutputCallId(snapshot, scriptedId),
      proof: capture => proveNativeOutputReceipt(capture, testInfo, readCodewhaleNativeOutput, {
        // The MCP result keeps both ends of the computed output in its preview.
        previewMarkers: [capture.output.firstMarker, capture.output.lastMarker],
        extraProof: () => {
          // The reader requires exactly one tool call and exactly one reply to it.
          const exchange = readMcpCallExchange(receiptLog)
          expect({ name: exchange.name, arguments: exchange.arguments }).toEqual({ name: 'inspect', arguments: { count: -1, enabled: false, text: '' } })
          expect(exchange.result).toEqual(generated.nativeResult)
        },
      }),
    })
  })
})

codewhaleTest('keeps the native Bash output path and limit after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openProviderAgent(leapmuxServer, context.workspaceId, CODEWHALE_AGENT, { directoryPrefix: 'codewhale-native-bash-cap-' })
  await openWorkspace(page, context.workspaceId)
  await captureNativeToolOutput(context, testInfo, {
    output: computedNativeToolOutput({ lineCount: 8000, padding: 30 }),
    callId: 'native-bash-first-cap',
    nativeCallId: (_request, scriptedId, snapshot) => codewhaleNativeOutputCallId(snapshot, scriptedId),
    proof: capture => proveNativeOutputReceipt(capture, testInfo, readCodewhaleNativeOutput, {
      extraProof: (receipt) => {
        const item = isObject(receipt.frame.payload) ? receipt.frame.payload.item : undefined
        expect(isObject(item) && isObject(item.metadata) ? item.metadata.artifact_id : undefined).toBeUndefined()
        // The model answers under the scripted call id; the runtime's own id lives
        // on the Worker's item frames alone.
        const excerpt = nativeToolResult(capture.request, capture.call.id)
        expect(codewhaleBashModelMatches(receipt.previewText, excerpt)).toBe(true)
      },
    }),
  })
})

import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { isObject } from '../../../src/lib/jsonPick'
import { CODEWHALE_AGENT, codewhaleTest, expect } from '../codewhale-fixtures'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { codewhaleBashModelMatches } from './bashModelOutput'
import { codewhaleMcpToolResult } from './mcpToolResult'
import { readCodewhaleNativeOutput } from './outputFilePaths'
import { nativeContext } from './scenarios'

codewhaleTest('keeps the native MCP output path and exact preview after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const directory = createTestDirectory('codewhale-native-mcp-output-path-')
  const generated = codewhaleMcpToolResult(computedNativeToolOutput({ lineCount: 8000, padding: 30 }), { approvedByUser: true })
  const receiptLog = join(directory, 'native-mcp-result-receipts.json')
  const server = writeMcpResultServer(directory, { receiptLog, inspectContent: generated.nativeResult.content })
  const home = leapmuxServer.agentEnv.CODEWHALE_HOME
  if (!home)
    throw new Error('The native Codewhale MCP case requires its private home.')
  await withNativeConfigurationFile({ path: join(home, 'mcp.json'), content: JSON.stringify({ servers: { result_probe: { command: process.execPath, args: [server] } } }), runDir: getGlobalState().tmpDir }, async () => {
    await openProviderAgent(leapmuxServer, context.workspaceId, CODEWHALE_AGENT, { workingDir: directory })
    await openWorkspace(page, context.workspaceId)
    await captureNativeToolOutput(context, testInfo, {
      output: generated.capture,
      callId: 'native-output-path',
      call: (_output, callId) => mcpToolCall(context.provider, callId, { server: 'result_probe', tool: 'inspect', input: { count: -1, enabled: false, text: '' } }),
      proof: capture => proveNativeOutputReceipt(capture, testInfo, readCodewhaleNativeOutput, {
        // The MCP result keeps both ends of the computed output in its preview.
        previewMarkers: [capture.output.firstMarker, capture.output.lastMarker],
        extraProof: () => {
          const receipts: unknown = JSON.parse(readFileSync(receiptLog, 'utf8'))
          if (!Array.isArray(receipts))
            throw new Error('The native Codewhale MCP case returned no receipt array.')
          const calls = receipts.filter(isObject).map(receipt => receipt.request).filter(isObject).filter(request => request.method === 'tools/call')
          expect(calls).toHaveLength(1)
          expect(calls[0]?.params).toMatchObject({ name: 'inspect', arguments: { count: -1, enabled: false, text: '' } })
          const params = isObject(calls[0]?.params) ? calls[0].params : undefined
          expect(params?.arguments).toEqual({ count: -1, enabled: false, text: '' })
          const replies = receipts.filter(isObject).map(receipt => receipt.reply).filter(isObject).filter(reply => reply.id === calls[0]?.id)
          expect(replies).toHaveLength(1)
          expect(replies[0]?.result).toEqual(generated.nativeResult)
        },
      }),
    })
  })
})

codewhaleTest('keeps the native Bash output path and limit after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openProviderAgent(leapmuxServer, context.workspaceId, CODEWHALE_AGENT, { workingDir: createTestDirectory('codewhale-native-bash-cap-') })
  await openWorkspace(page, context.workspaceId)
  await captureNativeToolOutput(context, testInfo, {
    output: computedNativeToolOutput({ lineCount: 8000, padding: 30 }),
    callId: 'native-bash-first-cap',
    proof: capture => proveNativeOutputReceipt(capture, testInfo, readCodewhaleNativeOutput, {
      extraProof: (receipt) => {
        const item = isObject(receipt.frame.payload) ? receipt.frame.payload.item : undefined
        expect(isObject(item) && isObject(item.metadata) ? item.metadata.artifact_id : undefined).toBeUndefined()
        const excerpt = nativeToolResult(capture.request, capture.nativeCallId)
        expect(codewhaleBashModelMatches(receipt.previewText, excerpt)).toBe(true)
      },
    }),
  })
})

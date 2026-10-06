import type { McpResultContent } from '../helpers/mcpResultServer'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { createTestDirectory } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { writeToolImage } from '../helpers/toolImages'
import { invokeDeepseekHarnessMcp, withDeepseekHarnessMcp } from './mcpScenarios'
import { proveDeepseekHarnessMixedMcpOutput } from './mcpToolResult'
import { readDeepseekHarnessNativeOutput } from './outputFilePaths'
import { nativeContext } from './scenarios'

deepseekHarnessTest('keeps native output paths and the exact inline preview after reload', async ({ authenticatedDeepseekHarnessWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  await captureNativeToolOutput(native, testInfo, {
    output: computedNativeToolOutput({ lineCount: 8000, padding: 30 }),
    callId: 'native-output-file-paths',
    proof: async (capture) => {
      const receipt = readDeepseekHarnessNativeOutput(capture.snapshot, capture.nativeCallId)
      expect(receipt.paths.length).toBeGreaterThan(0)
      for (const path of receipt.paths)
        assertPrivateNativePath(path, getGlobalState().tmpDir)
      expect(JSON.stringify(receipt.frame)).not.toContain(capture.output.omittedMarker)
      await testInfo.attach('deepseek-native-output-path-receipt', { body: JSON.stringify({ agentId: capture.agent.id, sessionId: capture.agent.agentSessionId, callId: capture.nativeCallId, paths: receipt.paths, previewText: receipt.previewText, frame: receipt.frame, supplement: receipt.supplement }), contentType: 'application/json' })
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: capture.nativeCallId,
        previewText: receipt.previewText,
        previewMarkers: ['Full formatted result stored at:'],
        paths: receipt.paths,
        status: receipt.status,
        prepareView: expandNativeResultView,
        workerProof: async () => {
          const snapshot = await readNativeMessageSnapshot(native, capture.agent.id)
          expect(snapshot.agentSessionId).toBe(capture.agent.agentSessionId)
          const current = readDeepseekHarnessNativeOutput(snapshot, capture.nativeCallId)
          expect(current.frame).toEqual(receipt.frame)
          expect(current.paths).toEqual(receipt.paths)
          expect(current.previewText).toBe(receipt.previewText)
          expect(current.message.content).toEqual(receipt.message.content)
        },
      })
    },
  })
})

for (const layout of ['omitted-middle-image', 'retained-end-images'] as const) {
  deepseekHarnessTest(`keeps native MCP preview paths and image order after reload for ${layout}`, async ({ authenticatedEmptyWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    const directory = createTestDirectory('deepseek-mcp-paths-')
    const imagePath = join(directory, writeToolImage(directory, layout))
    const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
    const image: McpResultContent = { type: 'image', path: imagePath }
    const content: McpResultContent[] = layout === 'omitted-middle-image'
      ? [{ type: 'text', text: output.text }, image, { type: 'text', text: output.text }]
      : [{ type: 'text', text: output.firstMarker }, image, { type: 'text', text: output.text }, image, { type: 'text', text: output.lastMarker }]
    const receiptLog = join(directory, 'receipts.json')
    const script = writeMcpResultServer(directory, { receiptLog, inspectContent: content })
    const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await withDeepseekHarnessMcp(native, { name: 'results', script, workingDir: directory }, async (privateContext) => {
      const callId = `native-mcp-paths-${layout}`
      const input = { count: 0, enabled: false, text: 'The exact native mixed result.' }
      const request = await invokeDeepseekHarnessMcp(privateContext, { server: 'results', tool: 'inspect', callId, input })
      await proveDeepseekHarnessMixedMcpOutput(privateContext, { callId, request, input, receiptLog, expected: content, firstMarker: output.firstMarker, omittedMarker: output.omittedMarker, retainedImages: layout === 'retained-end-images' ? 2 : 0, testInfo })
    })
  })
}

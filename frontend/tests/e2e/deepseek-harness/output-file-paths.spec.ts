import type { McpResultContent } from '../helpers/mcpResultServer'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { invokeNativeMcpTool } from '../helpers/mcpExecution'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { getGlobalState } from '../helpers/server'
import { writeToolImage } from '../helpers/toolImages'
import { withDeepseekHarnessMcp } from './mcpScenarios'
import { proveDeepseekHarnessMixedMcpOutput } from './mcpToolResult'
import { readDeepseekHarnessNativeOutput } from './outputFilePaths'
import { DEEPSEEK_HARNESS_AGENT, nativeContext } from './scenarios'

deepseekHarnessTest('keeps native output paths and the exact inline preview after reload', async ({ native }, testInfo) => {
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'native-output-file-paths',
    proof: async (capture) => {
      // The fields of the record that a reload must not change: the frame, the paths, the preview, and the row bytes.
      const readRecord = (snapshot: NativeMessageSnapshot) => {
        const receipt = readDeepseekHarnessNativeOutput(snapshot, capture.nativeCallId)
        return { frame: receipt.frame, paths: receipt.paths, previewText: receipt.previewText, content: receipt.message.content }
      }
      const receipt = readDeepseekHarnessNativeOutput(capture.snapshot, capture.nativeCallId)
      await testInfo.attach('deepseek-native-output-path-receipt', { body: JSON.stringify({ agentId: capture.agent.id, sessionId: capture.agent.agentSessionId, callId: capture.nativeCallId, paths: receipt.paths, previewText: receipt.previewText, frame: receipt.frame, supplement: receipt.supplement }), contentType: 'application/json' })
      // DeepSeek can declare one path for each output stream and one for its spill file, so the proof requires at
      // least one path, not exactly one. `proveNativeOutputReceipt` cannot run this proof, because its receipt check
      // (`checkNativeOutputReceipt`) requires exactly one declared path.
      expect(receipt.paths.length).toBeGreaterThan(0)
      for (const path of receipt.paths)
        assertPrivateNativePath(path, getGlobalState().tmpDir)
      expect(JSON.stringify(receipt.frame)).not.toContain(output.omittedMarker)
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: capture.nativeCallId,
        previewText: receipt.previewText,
        // The spill notice of DeepSeek is the fixed text that its preview always holds.
        previewMarkers: ['Full formatted result stored at:'],
        absentMarkers: [output.omittedMarker],
        paths: receipt.paths,
        status: receipt.status,
        prepareView: expandNativeResultView,
        workerProof: () => expectUnchangedNativeRecord(native, capture.agent, readRecord, readRecord(capture.snapshot)),
      })
    },
  })
})

for (const layout of ['omitted-middle-image', 'retained-end-images'] as const) {
  deepseekHarnessTest(`keeps native MCP preview paths and image order after reload for ${layout}`, async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
    const directory = newProviderWorkingDir(DEEPSEEK_HARNESS_AGENT, 'deepseek-mcp-paths-')
    const imagePath = join(directory, writeToolImage(directory, layout))
    const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
    const image: McpResultContent = { type: 'image', path: imagePath }
    const content: McpResultContent[] = layout === 'omitted-middle-image'
      ? [{ type: 'text', text: output.text }, image, { type: 'text', text: output.text }]
      : [{ type: 'text', text: output.firstMarker }, image, { type: 'text', text: output.text }, image, { type: 'text', text: output.lastMarker }]
    const receiptLog = join(directory, 'receipts.json')
    const server = writeMcpResultServer(directory, { receiptLog, inspectContent: content })
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await withDeepseekHarnessMcp(context, { server, workingDir: directory }, async (privateContext) => {
      const callId = `native-mcp-paths-${layout}`
      const input = { count: 0, enabled: false, text: 'The exact native mixed result.' }
      const request = await invokeNativeMcpTool(privateContext, { server: server.name, tool: 'inspect', callId, input })
      await proveDeepseekHarnessMixedMcpOutput(privateContext, { callId, request, input, receiptLog, expected: content, firstMarker: output.firstMarker, omittedMarker: output.omittedMarker, retainedImages: layout === 'retained-end-images' ? 2 : 0, testInfo })
    })
  })
}

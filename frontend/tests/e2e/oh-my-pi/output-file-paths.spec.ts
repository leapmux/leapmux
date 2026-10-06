import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { expect, OH_MY_PI_AGENT, OH_MY_PI_YOLO, ohMyPiTest } from '../ohmypi-fixtures'
import { ohMyPiNativeOutput } from './nativeToolOutput'
import { nativeContext } from './scenarios'

ohMyPiTest('keeps the native Bash preview and opaque ID without deriving an output file path after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openProviderAgent(leapmuxServer, context.workspaceId, OH_MY_PI_AGENT, { workingDir: createTestDirectory('native-preview-omp-'), ...OH_MY_PI_YOLO })
  await openWorkspace(page, context.workspaceId)
  await captureNativeToolOutput(context, testInfo, {
    output: computedNativeToolOutput({ lineCount: 8000, padding: 30 }),
    callId: 'native-output-preview',
    proof: async (capture) => {
      const readReceipt = (snapshot: NativeMessageSnapshot) => ohMyPiNativeOutput(snapshot, capture.nativeCallId)
      const receipt = readReceipt(capture.snapshot)
      expect(receipt.previewText).not.toContain(capture.output.omittedMarker)
      expect(receipt.previewText).toContain(capture.output.lastMarker)
      await testInfo.attach('oh-my-pi-native-preview-receipt', { body: JSON.stringify({ agentId: capture.agent.id, sessionId: capture.agent.agentSessionId, callId: capture.nativeCallId, opaqueId: receipt.artifactId, paths: [], previewText: receipt.previewText, frame: receipt.frame }), contentType: 'application/json' })
      await proveNativeToolOutputFilePaths({
        context,
        callId: capture.nativeCallId,
        previewText: receipt.previewText,
        previewMarkers: [capture.output.lastMarker],
        absentMarkers: [capture.output.omittedMarker],
        paths: [],
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: () => expectUnchangedNativeRecord(context, capture.agent, readReceipt, receipt),
      })
    },
  })
})

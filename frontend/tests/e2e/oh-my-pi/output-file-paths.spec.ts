import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { expect, ohMyPiTest } from '../ohmypi-fixtures'
import { ohMyPiNativeOutput } from './nativeToolOutput'

ohMyPiTest('keeps the native Bash preview and opaque ID without deriving an output file path after reload', async ({ authenticatedEmptyWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, native.workspaceId, createTestDirectory('native-preview-omp-'), { agentProvider: native.provider, ...agentOpenOptions(agentSettings(native.provider)), optionValues: { permissionMode: 'yolo' } })
  await openWorkspace(page, native.workspaceId)
  await captureNativeToolOutput(native, testInfo, {
    output: computedNativeToolOutput({ lineCount: 8000, padding: 30 }),
    callId: 'native-output-preview',
    proof: async (capture) => {
      const receipt = ohMyPiNativeOutput(capture.snapshot, capture.nativeCallId)
      expect(receipt.previewText).not.toContain(capture.output.omittedMarker)
      expect(receipt.previewText).toContain(capture.output.lastMarker)
      await testInfo.attach('oh-my-pi-native-preview-receipt', { body: JSON.stringify({ agentId: capture.agent.id, sessionId: capture.agent.agentSessionId, callId: capture.nativeCallId, opaqueId: receipt.artifactId, paths: [], previewText: receipt.previewText, frame: receipt.frame }), contentType: 'application/json' })
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: capture.nativeCallId,
        previewText: receipt.previewText,
        previewMarkers: [capture.output.lastMarker],
        paths: [],
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: async () => {
          const snapshot = await readNativeMessageSnapshot(native, capture.agent.id)
          expect(snapshot.agentSessionId).toBe(capture.agent.agentSessionId)
          expect(ohMyPiNativeOutput(snapshot, capture.nativeCallId)).toEqual(receipt)
        },
      })
    },
  })
})

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_AGENT, codebuddyTest, expect } from '../codebuddy-fixtures'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { createTestDirectory } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { readCodeBuddyNativeOutput } from './outputFilePaths'

codebuddyTest('keeps the native filesystem path and exact inline preview after reload', async ({ authenticatedEmptyWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.CODEBUDDY }
  await openProviderAgent(leapmuxServer, native.workspaceId, CODEBUDDY_AGENT, { workingDir: createTestDirectory('native-output-path-') })
  await openWorkspace(page, native.workspaceId)
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'native-output-path',
    proof: async (capture) => {
      const receipt = readCodeBuddyNativeOutput(capture.snapshot, capture.nativeCallId)
      expect(receipt.paths).toHaveLength(1)
      assertPrivateNativePath(receipt.paths[0]!, getGlobalState().tmpDir)
      expect(receipt.previewText).not.toContain(capture.output.omittedMarker)
      const previewMarkers = [capture.output.firstMarker, capture.output.lastMarker].filter(marker => receipt.previewText.includes(marker))
      expect(previewMarkers.length).toBeGreaterThan(0)
      await testInfo.attach('codebuddy-code-native-output-path-receipt', { body: JSON.stringify({ agentId: capture.agent.id, sessionId: capture.agent.agentSessionId, callId: capture.nativeCallId, paths: receipt.paths, previewText: receipt.previewText, frame: receipt.frame }), contentType: 'application/json' })
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: capture.nativeCallId,
        previewText: receipt.previewText,
        previewMarkers,
        paths: receipt.paths,
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: async () => {
          const snapshot = await readNativeMessageSnapshot(native, capture.agent.id)
          expect(snapshot.agentSessionId).toBe(capture.agent.agentSessionId)
          const current = readCodeBuddyNativeOutput(snapshot, capture.nativeCallId)
          expect(current.frame).toEqual(receipt.frame)
          expect(current.content).toEqual(receipt.content)
          expect(current.paths).toEqual(receipt.paths)
          expect(current.previewText).toBe(receipt.previewText)
        },
      })
    },
  })
})

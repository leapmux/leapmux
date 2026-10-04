import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, grokTest, openGrokAgent } from '../grok-fixtures'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { readGrokNativeOutput } from './outputFilePaths'

grokTest('keeps the native filesystem path and exact inline preview after reload', async ({ authenticatedEmptyWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const opened = await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { approvalMode: 'always-approve' })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.GROK_BUILD }
  expect((await currentNativeAgent(native)).id).toBe(opened.agentId)
  const output = computedNativeToolOutput({ lineCount: 6000, padding: 48 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'native-grok-build-output-path',
    proof: async (capture) => {
      const receipt = readGrokNativeOutput(capture.snapshot, capture.nativeCallId)
      expect(receipt.paths).toHaveLength(1)
      assertPrivateNativePath(receipt.paths[0]!, getGlobalState().tmpDir)
      expect(receipt.previewText).not.toContain(capture.output.omittedMarker)
      const previewMarkers = [capture.output.firstMarker, capture.output.lastMarker].filter(marker => receipt.previewText.includes(marker))
      expect(previewMarkers.length).toBeGreaterThan(0)
      await testInfo.attach('grok-build-native-output-path-receipt', { body: JSON.stringify({ agentId: capture.agent.id, sessionId: capture.agent.agentSessionId, callId: capture.nativeCallId, paths: receipt.paths, previewText: receipt.previewText, frame: receipt.frame }), contentType: 'application/json' })
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
          const current = readGrokNativeOutput(snapshot, capture.nativeCallId)
          expect(current.frame).toEqual(receipt.frame)
          expect(current.content).toEqual(receipt.content)
          expect(current.paths).toEqual(receipt.paths)
          expect(current.previewText).toBe(receipt.previewText)
        },
      })
    },
  })
})

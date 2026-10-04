import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { copilotTest, expect } from '../copilot-fixtures'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { resolveNativeProcessOwnership } from '../helpers/nativeProcessOwnership'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { processExecutable } from '../helpers/processExecutable'
import { listProcesses } from '../helpers/processTree'
import { getGlobalState } from '../helpers/server'
import { applyPermissionPreset } from '../helpers/ui'
import { copilotNativeOutputPaths, copilotNativePreview, copilotOutputFileCreatorPid } from './outputFilePaths'

copilotTest('keeps the native shell file path, creator owner, and exact preview Copy after reload', async ({ authenticatedCopilotWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedCopilotWorkspace.workspaceId, provider: AgentProvider.GITHUB_COPILOT }
  const output = computedNativeToolOutput({ lineCount: 6000, padding: 48 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'native-github-copilot-output-path',
    prepare: () => applyPermissionPreset(page, 'bypass'),
    proof: async (capture) => {
      const originals = capture.snapshot.messages.filter(message => message.agentSessionId === capture.agent.agentSessionId && message.spanId === capture.nativeCallId)
      const frames = originals.map(nativeMessageBody)
      expect(frames.length).toBeGreaterThan(0)
      expect(JSON.stringify(frames)).not.toContain(output.omittedMarker)
      const receipt = copilotNativeOutputPaths(frames, capture.nativeCallId, capture.agent.agentSessionId)
      const { path, excerpt } = receipt
      const creatorPid = copilotOutputFileCreatorPid(path)
      const rows = listProcesses()
      const state = getGlobalState()
      const creatorExecutable = await processExecutable(creatorPid)
      const ownership = resolveNativeProcessOwnership(rows, creatorPid, state.binaryPath)
      const workerExecutable = await processExecutable(ownership.workerPid)
      await testInfo.attach('copilot-native-output-path-creator', {
        body: JSON.stringify({
          agentId: capture.agent.id,
          sessionId: capture.agent.agentSessionId,
          callId: capture.nativeCallId,
          path,
          shellId: receipt.shellId,
          creatorPid,
          creatorExecutable,
          workerExecutable,
          workerDataDir: leapmuxServer.dataDir,
          creator: rows.find(row => row.pid === creatorPid),
          worker: rows.find(row => row.pid === ownership.workerPid),
          ownership,
        }),
        contentType: 'application/json',
      })
      assertPrivateNativePath(path, getGlobalState().tmpDir)
      if (receipt.preview !== undefined)
        expect(receipt.preview).not.toContain(output.omittedMarker)
      expect(excerpt).not.toContain(output.omittedMarker)
      const previewText = copilotNativePreview(receipt)
      await testInfo.attach('copilot-native-path-preview-receipt', { body: JSON.stringify({ agentId: capture.agent.id, sessionId: capture.agent.agentSessionId, callId: capture.nativeCallId, path, previewText, receipt, frames }), contentType: 'application/json' })
      expect(previewText).toContain(output.firstMarker)
      expect(previewText).not.toContain(output.lastMarker)
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: capture.nativeCallId,
        previewText,
        previewMarkers: [output.firstMarker],
        paths: [path],
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: async () => {
          const current = await currentNativeAgent(native)
          expect(current.id).toBe(capture.agent.id)
          expect(current.agentSessionId).toBe(capture.agent.agentSessionId)
          const snapshot = await readNativeMessageSnapshot(native, current.id)
          const rows = snapshot.messages.filter(message => message.agentSessionId === capture.agent.agentSessionId && message.spanId === capture.nativeCallId)
          expect(rows.map(nativeMessageBody)).toEqual(frames)
          expect(rows.map(row => row.content)).toEqual(originals.map(row => row.content))
          expect(copilotNativeOutputPaths(rows.map(nativeMessageBody), capture.nativeCallId, capture.agent.agentSessionId)).toEqual(receipt)
        },
      })
    },
  })
})

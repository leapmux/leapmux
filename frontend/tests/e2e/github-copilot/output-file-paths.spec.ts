import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { nativeMessageBody } from '../helpers/nativeMessages'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { resolveNativeProcessOwnership } from '../helpers/nativeProcessOwnership'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { processExecutable } from '../helpers/processExecutable'
import { listProcesses } from '../helpers/processTree'
import { getGlobalState } from '../helpers/server'
import { copilotNativeOutputPaths, copilotNativePreview, copilotOutputFileCreatorPid } from './outputFilePaths'
import { bypassToolRequests } from './scenarios'

copilotTest('keeps the native shell file path, creator owner, and exact preview Copy after reload', async ({ native, leapmuxServer }, testInfo) => {
  const output = computedNativeToolOutput({ lineCount: 6000, padding: 48 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'native-github-copilot-output-path',
    prepare: () => bypassToolRequests(native),
    proof: async (capture) => {
      // The original rows of the call: their frames, their bytes, and the receipt that the frames give.
      const readCall = (snapshot: NativeMessageSnapshot) => {
        const rows = snapshot.messages.filter(message => message.agentSessionId === capture.agent.agentSessionId && message.spanId === capture.nativeCallId)
        const frames = rows.map(nativeMessageBody)
        return { frames, contents: rows.map(row => row.content), receipt: copilotNativeOutputPaths(frames, capture.nativeCallId, capture.agent.agentSessionId) }
      }
      const original = readCall(capture.snapshot)
      const { frames, receipt } = original
      expect(frames.length).toBeGreaterThan(0)
      expect(JSON.stringify(frames)).not.toContain(output.omittedMarker)
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
        absentMarkers: [output.omittedMarker],
        paths: [path],
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: () => expectUnchangedNativeRecord(native, capture.agent, readCall, original),
      })
    },
  })
})

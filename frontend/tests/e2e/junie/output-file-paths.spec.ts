import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { randomUUID } from 'node:crypto'
import { withCleanup } from '../helpers/cleanup'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { nativeTextStep } from '../helpers/nativeScenario'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput, nativeOutputFileCommand } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { newProviderWorkingDir, openProviderAgent } from '../helpers/workspace'
import { expect, junieTest } from '../junie-fixtures'
import { waitForJunieOutputFilePaths } from './outputFilePathReadiness'
import { junieNativeNoticePath, readJunieNativeOutputPaths } from './outputFilePaths'
import { JUNIE_AGENT, nativeContext } from './scenarios'
import { junieNativeOutputFileCallId } from './toolCallIdentity'

junieTest('keeps the exact native command preview and pointer-only file path after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const workingDirectory = newProviderWorkingDir(JUNIE_AGENT, 'native-path-junie-')
  await openProviderAgent(leapmuxServer, context.workspaceId, JUNIE_AGENT, { workingDir: workingDirectory, optionValues: { brave_mode: 'on' } })
  await openWorkspace(page, context.workspaceId)
  const gate = `junie-native-path-final-${randomUUID()}`
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  let capturedPath: string | undefined
  await withCleanup(async () => {
    await captureNativeToolOutput(context, testInfo, {
      output,
      callId: 'native-output-file-path',
      nativeCallId: (_request, _scriptedId, snapshot) => junieNativeOutputFileCallId(snapshot, nativeOutputFileCommand(output), workingDirectory),
      finalStep: { ...nativeTextStep(context, 'The native large tool output ended.'), gate },
      beforeIdleProof: capture => withCleanup(async () => {
        await modelScript.waitForGate(gate)
        const modelPreview = nativeToolResult(capture.request, capture.call.id)
        capturedPath = junieNativeNoticePath(modelPreview, capture.agent.agentSessionId)
        const { snapshot, receipt } = await waitForJunieOutputFilePaths({
          agentId: capture.agent.id,
          sessionId: capture.agent.agentSessionId,
          callId: capture.nativeCallId,
          command: nativeOutputFileCommand(output),
          workingDirectory,
        }, {
          readSnapshot: () => readNativeMessageSnapshot(context, capture.agent.id),
          waitUntilSettled: async observe => expect.poll(observe).toBe(true),
        })
        expect(receipt.paths).toEqual([capturedPath])
        assertPrivateNativePath(capturedPath, getGlobalState().tmpDir)
        expect(modelPreview).not.toContain(output.omittedMarker)
        await testInfo.attach('junie-native-initial-path-owner', { body: JSON.stringify({ agentId: snapshot.agentId, sessionId: snapshot.agentSessionId, callId: capture.nativeCallId, workingDirectory, paths: receipt.paths, previewText: receipt.previewText, modelPreview, frame: receipt.frame, supplement: receipt.supplement }), contentType: 'application/json' })
      }, async () => {
        await modelScript.releaseGateIfHeld(gate)
      }),
      proof: async (capture) => {
        // The fields of the record that a reload must not change.
        const readRecord = (snapshot: NativeMessageSnapshot) => {
          const current = readJunieNativeOutputPaths(snapshot, capture.nativeCallId)
          return { frame: current.frame, supplement: current.supplement, paths: current.paths, previewText: current.previewText, content: current.message.content }
        }
        const receipt = readRecord(capture.snapshot)
        expect(receipt.paths).toEqual([capturedPath])
        expect(receipt.previewText).not.toContain(output.omittedMarker)
        await proveNativeToolOutputFilePaths({
          context,
          callId: capture.nativeCallId,
          previewText: receipt.previewText,
          previewMarkers: [output.firstMarker, output.lastMarker],
          absentMarkers: [output.omittedMarker],
          paths: receipt.paths,
          status: 'completed',
          prepareView: expandNativeResultView,
          workerProof: async (reloaded) => {
            await expectUnchangedNativeRecord(context, capture.agent, readRecord, receipt)
            await testInfo.attach(`junie-native-path-owner-${reloaded ? 'reload' : 'initial'}`, { body: JSON.stringify({ agentId: capture.agent.id, sessionId: capture.agent.agentSessionId, callId: capture.nativeCallId, paths: receipt.paths, previewText: receipt.previewText, frame: receipt.frame, supplement: receipt.supplement }), contentType: 'application/json' })
          },
        })
      },
    })
  }, async () => {
    await modelScript.releaseGateIfHeld(gate)
  })
})

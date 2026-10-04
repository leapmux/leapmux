import { randomUUID } from 'node:crypto'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { withCleanup } from '../helpers/cleanup'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { nativeTextStep } from '../helpers/nativeScenario'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput, nativeOutputFileCommand } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { createTestDirectory } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { expect, junieTest } from '../junie-fixtures'
import { waitForJunieOutputFilePaths } from './outputFilePathReadiness'
import { junieNativeNoticePath, readJunieNativeOutputPaths } from './outputFilePaths'
import { nativeContext } from './scenarios'
import { junieNativeOutputFileCallId } from './toolCallIdentity'

junieTest('keeps the exact native command preview and pointer-only file path after reload', async ({ authenticatedEmptyWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const workingDirectory = createTestDirectory('native-path-junie-')
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, native.workspaceId, workingDirectory, { agentProvider: native.provider, ...agentOpenOptions(agentSettings(native.provider)), optionValues: { brave_mode: 'on' } })
  await openWorkspace(page, native.workspaceId)
  const gate = `junie-native-path-final-${randomUUID()}`
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  let capturedPath: string | undefined
  await withCleanup(async () => {
    await captureNativeToolOutput(native, testInfo, {
      output,
      callId: 'native-output-file-path',
      nativeCallId: (_request, _scriptedId, snapshot) => junieNativeOutputFileCallId(snapshot, nativeOutputFileCommand(output), workingDirectory),
      finalStep: { ...nativeTextStep(native, 'The native large tool output ended.'), gate },
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
          readSnapshot: () => readNativeMessageSnapshot(native, capture.agent.id),
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
        const receipt = readJunieNativeOutputPaths(capture.snapshot, capture.nativeCallId)
        expect(receipt.paths).toEqual([capturedPath])
        expect(receipt.previewText).not.toContain(output.omittedMarker)
        await proveNativeToolOutputFilePaths({
          context: native,
          callId: capture.nativeCallId,
          previewText: receipt.previewText,
          previewMarkers: [output.firstMarker, output.lastMarker],
          paths: receipt.paths,
          status: 'completed',
          prepareView: expandNativeResultView,
          workerProof: async (reloaded) => {
            const snapshot = await readNativeMessageSnapshot(native, capture.agent.id)
            expect(snapshot.agentId).toBe(capture.agent.id)
            expect(snapshot.agentSessionId).toBe(capture.agent.agentSessionId)
            const current = readJunieNativeOutputPaths(snapshot, capture.nativeCallId)
            expect(current.frame).toEqual(receipt.frame)
            expect(current.supplement).toEqual(receipt.supplement)
            expect(current.paths).toEqual(receipt.paths)
            expect(current.previewText).toBe(receipt.previewText)
            expect(current.message.content).toEqual(receipt.message.content)
            await testInfo.attach(`junie-native-path-owner-${reloaded ? 'reload' : 'initial'}`, { body: JSON.stringify({ agentId: snapshot.agentId, sessionId: snapshot.agentSessionId, callId: capture.nativeCallId, paths: current.paths, previewText: current.previewText, frame: current.frame, supplement: current.supplement }), contentType: 'application/json' })
          },
        })
      },
    })
  }, async () => {
    await modelScript.releaseGateIfHeld(gate)
  })
})

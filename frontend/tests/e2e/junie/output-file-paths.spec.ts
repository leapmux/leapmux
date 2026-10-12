import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { nativeTextStep } from '../helpers/nativeScenario'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput, nativeOutputFileCommand } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { junieTest } from '../junie-fixtures'
import { waitForJunieOutputFilePaths } from './outputFilePathReadiness'
import { junieNativeNoticePath, readJunieNativeOutputReceipt } from './outputFilePaths'
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
      // The shared proof requires the one private path, a preview with both computed ends and without the omitted
      // middle, and the unchanged Worker record, before and after a reload.
      proof: capture => proveNativeOutputReceipt(capture, testInfo, readJunieNativeOutputReceipt, {
        previewMarkers: [output.firstMarker, output.lastMarker],
        extraProof: receipt => expect(receipt.paths).toEqual([capturedPath]),
      }),
    })
  }, async () => {
    await modelScript.releaseGateIfHeld(gate)
  })
})

import type { TestInfo } from '@playwright/test'
import type { NativeToolOutputCapture } from '../helpers/nativeToolOutputScenario'
import { expect } from '@playwright/test'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { getGlobalState } from '../helpers/server'
import { readOpenCodeNativeOutput } from './outputFilePaths'

/** Prove the original native preview and declared family path after reload. */
export async function proveOpenCodeOutputFilePaths(capture: NativeToolOutputCapture, testInfo: TestInfo): Promise<void> {
  const native = capture.context
  const receipt = readOpenCodeNativeOutput(capture.snapshot, capture.nativeCallId)
  expect(receipt.paths).toHaveLength(1)
  const path = receipt.paths[0]
  if (!path)
    throw new Error('The native result requires its declared path.')
  assertPrivateNativePath(path, getGlobalState().tmpDir)
  expect(receipt.previewText).not.toContain(capture.output.omittedMarker)
  const previewMarkers = [capture.output.firstMarker, capture.output.lastMarker].filter(marker => receipt.previewText.includes(marker))
  expect(previewMarkers.length).toBeGreaterThan(0)
  await testInfo.attach('native-output-path-receipt', { body: JSON.stringify({ agentId: capture.agent.id, sessionId: capture.agent.agentSessionId, callId: capture.nativeCallId, paths: receipt.paths, previewText: receipt.previewText, frame: receipt.frame }), contentType: 'application/json' })
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
      const current = readOpenCodeNativeOutput(snapshot, capture.nativeCallId)
      expect(current.frame).toEqual(receipt.frame)
      expect(current.content).toEqual(receipt.content)
      expect(current.paths).toEqual(receipt.paths)
      expect(current.previewText).toBe(receipt.previewText)
    },
  })
}

/**
 * Muse reports a filesystem path for a large saved tool output.
 *
 * The native tool call item carries an outputRef path; LeapMux stores it with the native
 * result, shows the path before the original preview, and copies the preview text.
 */
import type { TestInfo } from '@playwright/test'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { expect } from '@playwright/test'
import { museItem } from '../../../src/components/chat/providers/muse/protocol'
import { AgentProvider, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickString } from '../../../src/lib/jsonPick'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { presentPreviewMarkers, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { getGlobalState } from '../helpers/server'
import { chatScrollContainer } from '../helpers/ui'
import { museTest } from '../muse-fixtures'

/** Read the outputRef path of the one completed tool call item of the captured call. */
function museOutputRefPath(snapshot: NativeMessageSnapshot, callId: string): string {
  const paths = snapshot.messages
    .filter(message => message.agentProvider === AgentProvider.MUSE_CODE && message.source === MessageSource.AGENT
      && message.agentSessionId === snapshot.agentSessionId)
    .map(message => museItem(nativeMessageBody(message)))
    .filter((item): item is Record<string, unknown> => item?.callId === callId)
    .map(item => isObject(item.outputRef) ? pickString(item.outputRef, 'path') : '')
    .filter(path => path.trim() !== '')
  if (new Set(paths).size !== 1 || !paths[0])
    throw new Error(`The native Muse output reference of ${callId} must state exactly one path.`)
  return paths[0]
}

/** The visible output text of the one completed tool call item of the captured call. */
function museNativePreview(snapshot: NativeMessageSnapshot, callId: string): string {
  const previews = snapshot.messages
    .filter(message => message.agentProvider === AgentProvider.MUSE_CODE && message.source === MessageSource.AGENT
      && message.agentSessionId === snapshot.agentSessionId)
    .map(message => museItem(nativeMessageBody(message)))
    .filter((item): item is Record<string, unknown> => item?.callId === callId)
    .map(item => pickString(item, 'visibleOutput'))
    .filter(text => text !== '')
  return previews.at(-1) ?? ''
}

museTest('stores the native output path beside its original preview', async ({ native }, testInfo: TestInfo) => {
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'muse-output-paths',
    finalStep: { text: 'The native large tool output was stored with its path.' },
    proof: async (capture) => {
      const path = museOutputRefPath(capture.snapshot, capture.nativeCallId)
      assertPrivateNativePath(path, getGlobalState().tmpDir)
      // The row draws the NATIVE preview, which the host may cut before the last
      // line: state only the markers that preview actually holds.
      const previewText = museNativePreview(capture.snapshot, capture.nativeCallId)
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: capture.nativeCallId,
        previewText,
        previewMarkers: presentPreviewMarkers(previewText, output),
        paths: [path],
        status: 'completed',
        // The 8000-line preview collapses, and the chat renders only rows near the
        // scroll position: expand the result and reveal the row before counting.
        revealView: () => chatScrollContainer(native.page).evaluate(element => element.scrollTo({ top: 0, behavior: 'instant' })),
        prepareView: async result => expandNativeResultView(result),
        workerProof: async () => {
          const agent = await currentNativeAgent(native)
          const snapshot = await readNativeMessageSnapshot(native, agent.id)
          expect(museOutputRefPath(snapshot, capture.nativeCallId)).toBe(path)
        },
      })
    },
  })
})

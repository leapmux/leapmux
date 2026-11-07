import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { NativeToolOutput } from '../helpers/nativeToolOutput'
import { readFileSync, writeFileSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect } from '@playwright/test'
import { antigravityCommandOutput } from '../../../src/components/chat/providers/antigravity/extractors/execute'
import { pickObject, pickString } from '../../../src/lib/jsonPick'
import { antigravityTest } from '../antigravity-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { heldToolScript } from '../helpers/nativeLifecycle'
import { nativeMessageBody, readNativeMessageSnapshot, readNativeToolOutputRecord } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput, nativeOutputFileCommand } from '../helpers/nativeToolOutputScenario'
import { backgroundBashToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { quotePosixShellArgument, uniqueMarker } from '../helpers/shellArguments'
import { chatScrollContainer } from '../helpers/ui'
import { antigravityNativeSessionDir } from './nativeStore'
import { readAntigravityToolOutput } from './toolResult'

function commandRecord(snapshot: NativeMessageSnapshot, callId: string) {
  const record = readNativeToolOutputRecord(snapshot, {
    callId,
    spanId: callId,
    accepts: frame => frame.type === 'CORTEX_STEP_TYPE_RUN_COMMAND' && frame.status === 'CORTEX_STEP_STATUS_DONE'
      && pickString(pickObject(pickObject(frame, 'metadata'), 'toolCall'), 'id') === callId,
  })
  const frames = snapshot.messages.filter(message => message.agentSessionId === snapshot.agentSessionId && message.spanId === callId).map(message => nativeMessageBody(message))
  return {
    frames,
    frame: record.frame,
    content: record.message.content,
  }
}

function completedCommandPreview(snapshot: NativeMessageSnapshot, callId: string): string {
  const stored = commandRecord(snapshot, callId)
  return pickString(pickObject(pickObject(stored.frame, 'runCommand'), 'combinedOutput'), 'full')
}

for (const background of [false, true]) {
  antigravityTest(`keeps the native inline record and output path for a ${background ? 'background' : 'regular'} command`, async ({ native, modelScript }, testInfo) => {
    // Native background completion can wake the model after its initial receipt.
    await modelScript.fallback({ text: 'The native output task finished.' })
    const output = computedNativeToolOutput({ prefix: 'ANTIGRAVITYNATIVE', lineCount: 8000, padding: 12 })
    const initial = await currentNativeAgent(native)
    const marker = uniqueMarker()
    const releaseFile = join(initial.workingDir, `output-release-${marker}`)
    const startedFile = join(initial.workingDir, `output-started-${marker}`)
    const capture = await withCleanup(() => captureNativeToolOutput(native, testInfo, {
      output,
      callId: 'antigravity-native-preview',
      ...(background
        ? {
            call: (value: NativeToolOutput, id: string) => backgroundBashToolCall(native.provider, id, `${nativeOutputFileCommand(value)} && node -e ${quotePosixShellArgument(heldToolScript({ startedFile, releaseFile }))}`),
          }
        : {}),
      nativeCallId: (_request, id) => id,
      beforeIdleProof: async (proof) => {
        if (background)
          writeFileSync(releaseFile, '')
        // Root idle can precede background output. Require the owned final record.
        await retryUntilPass(async () => {
          const snapshot = await readNativeMessageSnapshot(native, proof.agent.id)
          commandRecord(snapshot, proof.nativeCallId)
        })
      },
      proof: async (proof) => {
        const receipt = readAntigravityToolOutput(proof.request, proof.call.id)
        expect(receipt).toMatch(/The tool's output was truncated|Tool is running as a background task with task id:/)
        expect(receipt).not.toContain(output.omittedMarker)
        if (background)
          expect(receipt).toContain('Tool is running as a background task with task id:')
        const preview = completedCommandPreview(proof.snapshot, proof.nativeCallId)
        expect(preview).not.toContain(output.omittedMarker)
        const uri = /full output in a file at (file:\/\/[^\\\s]+output\.txt)/.exec(preview)?.[1]
        if (!uri)
          throw new Error('The native truncated result has no full-output URI.')
        expect(readFileSync(fileURLToPath(uri), 'utf8')).toContain(output.omittedMarker)
      },
    }), async () => {
      if (background)
        writeFileSync(releaseFile, '')
    })
    const next = await sendNativeAnswer(native, 'Read the native context after the large result.', 'The native tool output context reached the model.')
    const masked = readAntigravityToolOutput(next, capture.call.id)
    expect(masked).toMatch(/The tool's output was truncated|Tool is running as a background task with task id:/)
    expect(masked).not.toContain(output.omittedMarker)
    // The first model receipt can describe a background task. The stored command
    // instead holds the final preview that the browser renders.
    const preview = completedCommandPreview(capture.snapshot, capture.nativeCallId)
    expect(preview).toContain('The tool\'s output was truncated')
    const uri = /full output in a file at (file:\/\/[^\\\s]+output\.txt)/.exec(preview)?.[1]
    if (!uri)
      throw new Error('The stored native command has no full-output URI.')
    const path = fileURLToPath(uri)
    const agent = await currentNativeAgent(native)
    expect(agent.id).toBe(capture.agent.id)
    expect(agent.agentSessionId).toBe(capture.agent.agentSessionId)
    expect(dirname(dirname(path))).toBe(join(antigravityNativeSessionDir(native, agent), '.system_generated', 'steps'))
    expect(basename(path)).toBe('output.txt')
    // The final stored LS step holds the preview and its full-output URI.
    const readStored = (snapshot: NativeMessageSnapshot) => commandRecord(snapshot, capture.nativeCallId)
    const stored = readStored(capture.snapshot)
    expect(JSON.stringify(stored.frame)).not.toContain(output.omittedMarker)
    expect(JSON.stringify(stored.frame)).toContain(path)
    await testInfo.attach('antigravity-native-model-mask', {
      body: JSON.stringify({
        agentId: agent.id,
        sessionId: agent.agentSessionId,
        nativeCallId: capture.nativeCallId,
        nativeOutputPath: path,
        masked,
        previewText: antigravityCommandOutput(preview),
        paths: [path],
        originalFrames: stored.frames,
      }),
      contentType: 'application/json',
    })
    await proveNativeToolOutputFilePaths({
      context: native,
      callId: capture.nativeCallId,
      previewText: antigravityCommandOutput(preview),
      paths: [path],
      status: 'completed',
      // Background wake messages can move this result outside the virtualized view.
      revealView: () => chatScrollContainer(native.page).evaluate(element => element.scrollTo({ top: 0, behavior: 'instant' })),
      prepareView: expandNativeResultView,
      previewMarkers: [output.firstMarker, output.lastMarker],
      absentMarkers: [output.omittedMarker],
      workerProof: () => expectUnchangedNativeRecord(native, capture.agent, readStored, stored),
    })
  })
}

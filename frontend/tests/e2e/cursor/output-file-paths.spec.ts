import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { Buffer } from 'node:buffer'
import { createHash } from 'node:crypto'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { cursorTest } from '../cursor-fixtures'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { nativeOutputFileCommand } from '../helpers/nativeToolOutputScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chatScrollContainer } from '../helpers/ui'
import { cursorNativeToolOutput, runCursorNativeOperations } from './nativeExecutionScenario'

cursorTest('retains complete inline output for the selected native shell route after reload', async ({ native }, testInfo) => {
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  const callId = 'cursor-native-inline-output'
  const command = nativeOutputFileCommand(output)
  await runCursorNativeOperations(native, [bashToolCall(native.provider, callId, command)], output.lastMarker)
  const agent = await currentNativeAgent(native)
  const snapshot = await readNativeMessageSnapshot(native, agent.id)
  const exact = await cursorNativeToolOutput(native, callId)
  await testInfo.attach('cursor-native-inline-output-records', { body: JSON.stringify({ callId, sessionId: agent.agentSessionId, previewText: output.text, exact, messages: snapshot.messages.map(nativeMessageBody), modelStatus: await native.modelScript.status() }, null, 2), contentType: 'application/json' })
  if (!isObject(exact) || exact.exitCode !== 0 || typeof exact.stdout !== 'string' || exact.stderr !== '')
    throw new Error('The selected native Cursor shell route did not return its complete successful output.')
  const text = exact.stdout
  const bytes = Buffer.from(text, 'utf8')
  await testInfo.attach('cursor-native-inline-output', { body: text, contentType: 'text/plain' })
  await testInfo.attach('cursor-native-output-limit-proof', { body: JSON.stringify({ route: 'native-shell', callId, sessionId: agent.agentSessionId, byteSize: bytes.length, digest: createHash('sha256').update(bytes).digest('hex'), nativeReference: exact }, null, 2), contentType: 'application/json' })
  expect(text).toBe(output.text)
  expect(exact).not.toHaveProperty('fullOutputPath')
  const callFrames = (current: NativeMessageSnapshot) => current.messages.filter(message => message.spanId === callId).map(nativeMessageBody)
  await proveNativeToolOutputFilePaths({
    context: native,
    callId,
    previewText: output.text,
    previewMarkers: [output.firstMarker, output.lastMarker],
    paths: [],
    status: 'completed',
    // The chat follows the end of the turn and renders only the rows near its scroll position. The result row of this
    // single-exchange turn has no element there, so the chat scrolls to the top before the proof counts the row.
    revealView: () => chatScrollContainer(native.page).evaluate(element => element.scrollTo({ top: 0, behavior: 'instant' })),
    prepareView: async (result) => {
      await expandNativeResultView(result)
      // Copy copies the complete output, which holds the omitted middle line, so that line cannot be an absent marker.
      // The expanded row shows the head and the tail of the output, and not the middle line.
      await expect(result).not.toContainText(output.omittedMarker)
    },
    workerProof: async () => {
      await expectUnchangedNativeRecord(native, agent, callFrames, callFrames(snapshot))
      expect(await cursorNativeToolOutput(native, callId)).toEqual(exact)
    },
  })
})

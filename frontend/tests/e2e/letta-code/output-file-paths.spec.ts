import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { Buffer } from 'node:buffer'
import { expect } from '@playwright/test'
import { LETTA_DELTA_FIELD, LETTA_DELTA_KIND } from '../../../src/generated/contracts/letta-protocol'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody, readNativeToolOutputRecord } from '../helpers/nativeMessages'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { lettaTest } from '../letta-fixtures'
import { lettaNativeFinalReturn, lettaNativeProgressReturn } from './nativeFinalReturn'
import { lettaOutputFilePath } from './outputFilePaths'

lettaTest('keeps the native shell preview, output path, and every phase record after reload', async ({ native, leapmuxServer }, testInfo) => {
  const output = computedNativeToolOutput({ lineCount: 6000, padding: 48 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'native-letta-code-output-path',
    proof: async (capture) => {
      const frames = capture.snapshot.messages.filter(message => message.agentSessionId === capture.agent.agentSessionId).map(nativeMessageBody)
      const home = leapmuxServer.agentEnv.HOME
      if (!home)
        throw new Error('The native Letta output path requires its private HOME.')
      const excerpt = nativeToolResult(capture.request, capture.nativeCallId)
      const original = lettaNativeFinalReturn(frames, capture.call.id, excerpt)
      const readToolRecords = (messages: typeof capture.snapshot.messages) => messages.filter(message => message.agentSessionId === capture.agent.agentSessionId).flatMap((message) => {
        const frame = nativeMessageBody(message)
        return isObject(frame) && frame[LETTA_DELTA_FIELD.ToolCallID] === capture.nativeCallId ? [{ message, frame }] : []
      })
      const records = readToolRecords(capture.snapshot.messages)
      const recordWire = (messages: typeof records) => messages.map(record => ({ id: record.message.id, content: Buffer.from(record.message.content).toString('base64'), compression: record.message.contentCompression }))
      const progress = records.filter(record => lettaNativeProgressReturn(record.frame))
      const lifecycle = records.filter(record => record.frame[LETTA_DELTA_FIELD.MessageType] === LETTA_DELTA_KIND.ClientToolEnd)
      expect(progress.length).toBeGreaterThan(0)
      expect(lifecycle).toHaveLength(1)
      for (const record of [...progress, ...lifecycle]) {
        expect(record.frame[LETTA_DELTA_FIELD.RunID]).toBe(original[LETTA_DELTA_FIELD.RunID])
        expect(record.frame[LETTA_DELTA_FIELD.ToolCallID]).toBe(capture.nativeCallId)
      }
      expect(Object.hasOwn(lifecycle[0]?.frame ?? {}, LETTA_DELTA_FIELD.ToolReturn)).toBe(false)
      expect(Object.hasOwn(lifecycle[0]?.frame ?? {}, LETTA_DELTA_FIELD.ToolReturns)).toBe(false)
      const endIndex = records.findIndex(record => record.frame[LETTA_DELTA_FIELD.MessageType] === LETTA_DELTA_KIND.ClientToolEnd)
      expect(endIndex).toBeGreaterThan(0)
      expect(records.slice(endIndex + 1).every(record => !lettaNativeProgressReturn(record.frame))).toBe(true)
      expect(records.at(-1)?.frame).toEqual(original)
      const wire = recordWire(records)
      await testInfo.attach('letta-code-native-tool-phase-records', { body: JSON.stringify({ sessionId: capture.agent.agentSessionId, callId: capture.nativeCallId, runId: original[LETTA_DELTA_FIELD.RunID], wire }), contentType: 'application/json' })
      expect(JSON.stringify(original)).not.toContain(output.omittedMarker)
      const path = lettaOutputFilePath(excerpt, home, capture.agent.workingDir)
      assertPrivateNativePath(path, home)
      // The records that a reload must not change: the final return, the wire bytes of every phase record, and the
      // stored record of the final return.
      const readPhases = (snapshot: NativeMessageSnapshot) => {
        const final = readNativeToolOutputRecord(snapshot, {
          callId: capture.nativeCallId,
          spanId: `letta-tool-${capture.nativeCallId}`,
          accepts: frame => frame.id === original.id && frame[LETTA_DELTA_FIELD.ToolCallID] === capture.nativeCallId
            && frame[LETTA_DELTA_FIELD.MessageType] === LETTA_DELTA_KIND.ToolReturnMessage && !lettaNativeProgressReturn(frame)
            && frame[LETTA_DELTA_FIELD.ToolReturn] === excerpt,
        })
        const sessionFrames = snapshot.messages.filter(message => message.agentSessionId === capture.agent.agentSessionId).map(nativeMessageBody)
        return {
          finalReturn: lettaNativeFinalReturn(sessionFrames, capture.call.id, excerpt),
          wire: recordWire(readToolRecords(snapshot.messages)),
          final: { frame: final.frame, content: final.message.content },
        }
      }
      const phases = readPhases(capture.snapshot)
      expect(phases.final.frame).toEqual(original)
      expect(excerpt).not.toContain(output.omittedMarker)
      await testInfo.attach('letta-native-output-path-preview', { body: JSON.stringify({ path, agentId: capture.agent.id, sessionId: capture.agent.agentSessionId, callId: capture.nativeCallId, previewText: excerpt, frame: original, wire }), contentType: 'application/json' })
      expect(excerpt).toContain(output.firstMarker)
      expect(excerpt).not.toContain(output.lastMarker)
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: capture.call.id,
        previewText: excerpt,
        previewMarkers: [output.firstMarker],
        absentMarkers: [output.omittedMarker],
        paths: [path],
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: () => expectUnchangedNativeRecord(native, capture.agent, readPhases, phases),
      })
    },
  })
})

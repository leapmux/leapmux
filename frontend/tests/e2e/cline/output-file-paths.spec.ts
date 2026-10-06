import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { isObject } from '../../../src/lib/jsonPick'
import { clineTest, expect } from '../cline-fixtures'
import { nativeMessageBody } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { clineNativeOutputLimit } from './nativeToolOutput'

clineTest('records the completed command output limit and retains its native head and tail after reload', async ({ native }, testInfo) => {
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'cline-native-output-limit',
    proof: async (capture) => {
      const exact = nativeToolResult(capture.request, capture.nativeCallId)
      const frames = capture.snapshot.messages.filter(message => message.spanId === capture.nativeCallId).map(nativeMessageBody)
      const operations = frames.filter(frame => isObject(frame) && frame.event === 'tool.finished' && frame.sessionId === capture.agent.agentSessionId
        && isObject(frame.payload) && frame.payload.toolCallId === capture.nativeCallId && frame.payload.toolName === 'run_commands')
      expect(operations).toHaveLength(1)
      const operation = operations[0]
      if (!operation)
        throw new Error('The Cline result has no exact completed native command operation.')
      const retained = clineNativeOutputLimit(JSON.stringify(operation))
      await testInfo.attach('cline-native-output-limit', { body: JSON.stringify({ nativeId: capture.nativeCallId, sessionId: capture.agent.agentSessionId, nativeReference: operation, modelProjection: exact, previewText: retained, retained }, null, 2), contentType: 'application/json' })
      expect(retained).toContain(output.firstMarker)
      expect(retained).toContain(output.lastMarker)
      expect(retained).not.toContain(output.omittedMarker)
      const callRows = (snapshot: NativeMessageSnapshot) => snapshot.messages.filter(message => message.spanId === capture.nativeCallId)
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: capture.nativeCallId,
        previewText: retained,
        previewMarkers: [output.firstMarker, output.lastMarker],
        absentMarkers: [output.omittedMarker],
        paths: [],
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: () => expectUnchangedNativeRecord(native, capture.agent, callRows, callRows(capture.snapshot)),
      })
    },
  })
})

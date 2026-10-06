import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { isObject } from '../../../src/lib/jsonPick'
import { expect, gooseTest } from '../goose-fixtures'
import { acpClosedToolCall } from '../helpers/acpToolFrame'
import { nativeMessageBody, nativeMessageSupplement } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bypassToolRequests } from './scenarios'
import { gooseTerminalOutputFileLimit } from './terminalOutputLimit'

gooseTest('records the native client terminal output limit and retains its exact output after reload', async ({ native }, testInfo) => {
  const output = computedNativeToolOutput({ lineCount: 6000, padding: 48 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'native-goose-output-limit',
    prepare: () => bypassToolRequests(native),
    proof: async (capture) => {
      const readLimit = (snapshot: NativeMessageSnapshot) => {
        const records = snapshot.messages.filter(message => message.agentSessionId === capture.agent.agentSessionId)
          .filter((message) => {
            const body = nativeMessageBody(message)
            return isObject(body) && acpClosedToolCall(body, capture.call.id, ['completed'])
          })
        expect(records).toHaveLength(1)
        const record = records[0]
        if (!record)
          throw new Error('The exact native Goose terminal result is absent.')
        return gooseTerminalOutputFileLimit(nativeMessageBody(record), nativeMessageSupplement(record), capture.call.id)
      }
      const limit = readLimit(capture.snapshot)
      const modelText = nativeToolResult(capture.request, capture.call.id)
      expect(modelText).toContain(limit.text)
      expect(modelText).not.toContain(output.omittedMarker)
      expect(limit.text).not.toContain(output.firstMarker)
      expect(limit.text).not.toContain(output.omittedMarker)
      expect(limit.text).toContain(output.lastMarker)
      await testInfo.attach('goose-native-client-terminal-limit', { body: JSON.stringify({ sessionId: capture.agent.agentSessionId, previewText: limit.text, ...limit }), contentType: 'application/json' })
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: capture.call.id,
        previewText: limit.text,
        previewMarkers: [output.lastMarker],
        absentMarkers: [output.omittedMarker],
        paths: [],
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: () => expectUnchangedNativeRecord(native, capture.agent, readLimit, limit),
      })
    },
  })
})

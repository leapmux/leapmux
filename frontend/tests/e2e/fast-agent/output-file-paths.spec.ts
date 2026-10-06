import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { expect, fastAgentTest } from '../fastagent-fixtures'
import { nativeMessageBody } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { fastAgentTerminalOutputFileLimit } from './nativeToolOutput'

fastAgentTest('records the native client terminal output limit and retains its exact tail after reload', async ({ native }, testInfo) => {
  const output = computedNativeToolOutput({ lineCount: 6000, padding: 48 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'native-fast-agent-output-limit',
    proof: async (capture) => {
      const command = capture.call.arguments?.command
      if (typeof command !== 'string')
        throw new Error('The native Fast Agent output scenario has no exact command.')
      const frames = capture.snapshot.messages.filter(message => message.agentSessionId === capture.agent.agentSessionId).map(nativeMessageBody)
      const modelText = nativeToolResult(capture.request, capture.call.id)
      const limit = fastAgentTerminalOutputFileLimit(frames, command, modelText)
      await testInfo.attach('fast-agent-native-client-terminal-limit', { body: JSON.stringify({ callId: limit.callId, modelCallId: capture.call.id, sessionId: capture.agent.agentSessionId, byteLimit: limit.byteLimit, previewText: limit.text, text: limit.text, modelText }), contentType: 'application/json' })
      expect(limit.text).not.toContain(output.firstMarker)
      expect(limit.text).not.toContain(output.omittedMarker)
      expect(limit.text).toContain(output.lastMarker)
      const callRows = (snapshot: NativeMessageSnapshot) => snapshot.messages.filter(message => message.spanId === limit.callId)
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: limit.callId,
        previewText: limit.text,
        previewMarkers: [output.lastMarker],
        absentMarkers: [output.omittedMarker],
        paths: [],
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: () => expectUnchangedNativeRecord(native, capture.agent, callRows, callRows(capture.snapshot)),
      })
    },
  })
})

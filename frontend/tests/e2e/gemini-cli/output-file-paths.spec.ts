import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { basename, dirname, join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject, pickString } from '../../../src/lib/jsonPick'
import { geminiTest } from '../gemini-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeMessageBody } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { geminiNativeProject } from './nativeStore'
import { readGeminiStoredToolRecord } from './toolRecord'
import { readGeminiToolOutput } from './toolResult'

geminiTest('keeps the native inline record after model-only masking without a Worker output path', async ({ native }, testInfo) => {
  const output = computedNativeToolOutput({ prefix: 'GEMININATIVE', lineCount: 8000, padding: 12 })
  const capture = await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'gemini-native-preview',
    nativeCallId: (_request, id) => `run_shell_command__${id}`,
    proof: async proof => expect(nativeToolResult(proof.request, proof.call.id)).toContain(output.omittedMarker),
  })
  const next = await sendNativeAnswer(native, 'Read the native context after the large result.', 'The native tool output context reached the model.')
  const masked = readGeminiToolOutput(next, capture.call.id)
  expect(masked).toContain('<tool_output_masked>')
  expect(masked).not.toContain(output.omittedMarker)
  const path = /^Output too large\. Full output available at: (.+)$/m.exec(masked)?.[1]
  if (!path)
    throw new Error('The native masked result has no model-only output path.')
  const agent = await currentNativeAgent(native)
  expect(agent.id).toBe(capture.agent.id)
  expect(agent.agentSessionId).toBe(capture.agent.agentSessionId)
  expect(dirname(path)).toBe(join(geminiNativeProject(native, agent), 'tool-outputs', `session-${agent.agentSessionId}`))
  expect(basename(path)).toMatch(new RegExp(`^run_shell_command_${capture.nativeCallId}_[a-z0-9]+\\.txt$`))
  // The stored rows of the call and the Gemini tool records that they hold.
  const readStored = (snapshot: NativeMessageSnapshot) => {
    const rows = snapshot.messages.filter(message => message.spanId === capture.nativeCallId)
    return { frames: rows.map(message => nativeMessageBody(message)), records: rows.map(readGeminiStoredToolRecord).filter(isObject) }
  }
  const stored = readStored(capture.snapshot)
  expect(stored.records).toHaveLength(1)
  expect(pickString(stored.records[0], 'id')).toBe(capture.nativeCallId)
  expect(pickString(stored.records[0], 'resultDisplay')).toBe(output.text)
  await testInfo.attach('gemini-native-model-mask', { body: JSON.stringify({ agentId: agent.id, sessionId: agent.agentSessionId, nativeCallId: capture.nativeCallId, modelOnlyPath: path, masked, previewText: output.text, paths: [], originalFrames: stored.frames }), contentType: 'application/json' })
  await proveNativeToolOutputFilePaths({
    context: native,
    callId: capture.nativeCallId,
    previewText: output.text,
    paths: [],
    status: 'completed',
    prepareView: expandNativeResultView,
    previewMarkers: [output.firstMarker, output.lastMarker],
    workerProof: () => expectUnchangedNativeRecord(native, capture.agent, readStored, stored),
  })
})

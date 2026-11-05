import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { readFileSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect } from '@playwright/test'
import { antigravityCommandOutput } from '../../../src/components/chat/providers/antigravity/extractors/execute'
import { isObject } from '../../../src/lib/jsonPick'
import { antigravityTest } from '../antigravity-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeMessageBody } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { antigravityNativeToolResult, readAntigravityToolOutput } from './toolResult'

antigravityTest('keeps the native inline record and exposes its full output path', async ({ native }, testInfo) => {
  const output = computedNativeToolOutput({ prefix: 'ANTIGRAVITYNATIVE', lineCount: 8000, padding: 12 })
  const capture = await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'antigravity-native-preview',
    nativeCallId: (_request, id) => id,
    proof: async (proof) => {
      const preview = antigravityNativeToolResult(proof.request, proof.call.id)
      expect(preview).not.toContain(output.omittedMarker)
      const uri = /full output in a file at (file:\/\/[^\\\s]+output\.txt)/.exec(preview)?.[1]
      if (!uri)
        throw new Error('The native truncated result has no full-output URI.')
      expect(readFileSync(fileURLToPath(uri), 'utf8')).toContain(output.omittedMarker)
    },
  })
  const next = await sendNativeAnswer(native, 'Read the native context after the large result.', 'The native tool output context reached the model.')
  const masked = readAntigravityToolOutput(next, capture.call.id)
  expect(masked).toContain('The tool\'s output was truncated')
  expect(masked).not.toContain(output.omittedMarker)
  const uri = /full output in a file at (file:\/\/[^\\\s]+output\.txt)/.exec(masked)?.[1]
  if (!uri)
    throw new Error('The native masked result has no full-output URI.')
  const path = fileURLToPath(uri)
  const agent = await currentNativeAgent(native)
  expect(agent.id).toBe(capture.agent.id)
  expect(agent.agentSessionId).toBe(capture.agent.agentSessionId)
  const home = native.leapmuxServer.agentEnv?.HOME
  if (!home)
    throw new Error('The Antigravity output scenario requires the isolated HOME.')
  expect(dirname(dirname(path))).toBe(join(home, '.gemini', 'antigravity-cli', 'brain', agent.agentSessionId, '.system_generated', 'steps'))
  expect(basename(path)).toBe('output.txt')
  // The final stored LS step holds the preview and its full-output URI.
  const readStored = (snapshot: NativeMessageSnapshot) => {
    const rows = snapshot.messages.filter(message => message.spanId === capture.nativeCallId)
    return {
      frames: rows.map(message => nativeMessageBody(message)),
      records: rows.filter((message) => {
        const body = nativeMessageBody(message)
        return isObject(body) && body.status === 'CORTEX_STEP_STATUS_DONE'
      }).map(message => nativeMessageBody(message)),
    }
  }
  const stored = readStored(capture.snapshot)
  expect(stored.records).toHaveLength(1)
  expect(JSON.stringify(stored.records[0])).not.toContain(output.omittedMarker)
  expect(JSON.stringify(stored.records[0])).toContain(path)
  await testInfo.attach('antigravity-native-model-mask', {
    body: JSON.stringify({
      agentId: agent.id,
      sessionId: agent.agentSessionId,
      nativeCallId: capture.nativeCallId,
      modelOnlyPath: path,
      masked,
      previewText: output.text,
      paths: [path],
      originalFrames: stored.frames,
    }),
    contentType: 'application/json',
  })
  await proveNativeToolOutputFilePaths({
    context: native,
    callId: capture.nativeCallId,
    previewText: antigravityCommandOutput(masked),
    paths: [path],
    status: 'completed',
    prepareView: expandNativeResultView,
    previewMarkers: [output.firstMarker, output.lastMarker],
    workerProof: () => expectUnchangedNativeRecord(native, capture.agent, readStored, stored),
  })
})

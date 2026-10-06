import { basename, dirname, join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject, pickString } from '../../../src/lib/jsonPick'
import { geminiTest } from '../gemini-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { geminiNativeProject } from './nativeStore'
import { nativeContext } from './scenarios'
import { readGeminiStoredToolRecord } from './toolRecord'
import { readGeminiToolOutput } from './toolResult'

geminiTest('keeps the native inline record after model-only masking without a Worker output path', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }, testInfo) => {
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const output = computedNativeToolOutput({ prefix: 'GEMININATIVE', lineCount: 8000, padding: 12 })
  const capture = await captureNativeToolOutput(context, testInfo, {
    output,
    callId: 'gemini-native-preview',
    nativeCallId: (_request, id) => `run_shell_command__${id}`,
    proof: async proof => expect(nativeToolResult(proof.request, proof.call.id)).toContain(output.omittedMarker),
  })
  const next = await sendNativeAnswer(context, 'Read the native context after the large result.', 'The native tool output context reached the model.')
  const masked = readGeminiToolOutput(next, capture.call.id)
  expect(masked).toContain('<tool_output_masked>')
  expect(masked).not.toContain(output.omittedMarker)
  const path = /^Output too large\. Full output available at: (.+)$/m.exec(masked)?.[1]
  if (!path)
    throw new Error('The native masked result has no model-only output path.')
  const agent = await currentNativeAgent(context)
  expect(agent.id).toBe(capture.agent.id)
  expect(agent.agentSessionId).toBe(capture.agent.agentSessionId)
  expect(dirname(path)).toBe(join(geminiNativeProject(context, agent), 'tool-outputs', `session-${agent.agentSessionId}`))
  expect(basename(path)).toMatch(new RegExp(`^run_shell_command_${capture.nativeCallId}_[a-z0-9]+\\.txt$`))
  const originals = capture.snapshot.messages.filter(message => message.spanId === capture.nativeCallId).map(message => nativeMessageBody(message))
  const workerProof = async () => {
    const snapshot = await readNativeMessageSnapshot(context, agent.id)
    const rows = snapshot.messages.filter(message => message.spanId === capture.nativeCallId)
    expect(rows.map(message => nativeMessageBody(message))).toEqual(originals)
    const records = rows.map(readGeminiStoredToolRecord).filter(isObject)
    expect(records).toHaveLength(1)
    expect(pickString(records[0], 'id')).toBe(capture.nativeCallId)
    expect(pickString(records[0], 'resultDisplay')).toBe(output.text)
    await testInfo.attach('gemini-native-model-mask', { body: JSON.stringify({ agentId: agent.id, sessionId: agent.agentSessionId, nativeCallId: capture.nativeCallId, modelOnlyPath: path, masked, previewText: output.text, paths: [], originalFrames: originals }), contentType: 'application/json' })
  }
  await proveNativeToolOutputFilePaths({
    context,
    callId: capture.nativeCallId,
    previewText: output.text,
    paths: [],
    status: 'completed',
    prepareView: expandNativeResultView,
    previewMarkers: [output.firstMarker, output.lastMarker],
    workerProof,
  })
})

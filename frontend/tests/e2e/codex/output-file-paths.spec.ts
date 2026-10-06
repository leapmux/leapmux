import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codexExecToolCall } from '../helpers/providerToolCalls'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { codexNativeOutputExcerpt } from './nativeToolOutput'
import { CODEX_AGENT, nativeContext } from './scenarios'

codexTest('preserves the native exec output limit and copies only its retained excerpt after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openProviderAgent(leapmuxServer, context.workspaceId, CODEX_AGENT, { directoryPrefix: 'codex-native-output-limit-' })
  await openWorkspace(page, context.workspaceId)
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  const callId = 'native-exec-native-output-limit'
  const source = `// @exec: {"max_output_tokens":300}\n${output.source}\ntext(completeOutput);`
  const { resultRequest } = await runNativeToolTurn(context, {
    toolCalls: [codexExecToolCall(callId, source)],
    prompt: 'Run the native exec large output once.',
    answer: 'The native exec excerpt completed.',
  })
  const agent = await currentNativeAgent(context)
  const snapshot = await readNativeMessageSnapshot(context, agent.id)
  const excerpt = nativeToolResult(resultRequest, callId)
  await testInfo.attach('codex-native-output-limit', { body: JSON.stringify({ callId, sessionId: agent.agentSessionId, excerpt, messages: snapshot.messages.map(nativeMessageBody), request: resultRequest }, null, 2), contentType: 'application/json' })
  expect(excerpt).toContain('Warning: truncated output')
  expect(excerpt).toContain(output.firstMarker)
  expect(excerpt).toContain(output.lastMarker)
  expect(excerpt).not.toContain(output.omittedMarker)
  const retainedText = codexNativeOutputExcerpt(excerpt)
  const callRows = (current: NativeMessageSnapshot) => current.messages.filter(message => message.spanId === callId)
  await proveNativeToolOutputFilePaths({
    context,
    callId,
    previewText: retainedText,
    previewMarkers: [output.firstMarker, output.lastMarker],
    absentMarkers: [output.omittedMarker],
    paths: [],
    status: 'completed',
    prepareView: expandNativeResultView,
    workerProof: () => expectUnchangedNativeRecord(context, agent, callRows, callRows(snapshot)),
  })
})

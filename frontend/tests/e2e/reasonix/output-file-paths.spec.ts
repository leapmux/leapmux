import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { Buffer } from 'node:buffer'
import { createHash, randomUUID } from 'node:crypto'
import process from 'node:process'
import { expect } from '@playwright/test'
import { MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeMessageBody, nativeMessageSupplement, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { reasonixTest } from '../reasonix-fixtures'
import { reasonixNativeOutput } from './nativeToolOutput'
import { bypassToolRequests } from './scenarios'

reasonixTest('keeps the native result record and exact inline Copy with no output file path after reload', async ({ native }, testInfo) => {
  await bypassToolRequests(native)
  const agent = await currentNativeAgent(native)
  const output = computedNativeToolOutput({ lineCount: 6000, padding: 48 })
  const program = `${output.source}\nprocess.stdout.write(completeOutput);`
  const command = `${quotePosixShellArgument(process.execPath)} -e ${quotePosixShellArgument(program)}`
  expect(command).not.toContain(output.omittedMarker)
  const callId = `native-record-${randomUUID()}`
  await runNativeToolTurn(native, {
    toolCalls: [bashToolCall(native.provider, callId, command)],
    prompt: 'Run the real large-output command once.',
    answer: 'The native record scenario ended.',
  })
  const readNativeOutput = (snapshot: NativeMessageSnapshot) => {
    const records = snapshot.messages.filter(message => message.source === MessageSource.AGENT
      && message.agentSessionId === agent.agentSessionId && message.spanId === callId)
      .filter((message) => {
        const body = nativeMessageBody(message)
        return typeof body === 'object' && body !== null && 'status' in body && body.status === 'completed'
      })
    expect(records).toHaveLength(1)
    const message = records[0]
    if (!message)
      throw new Error('The native Reasonix result record is absent.')
    return reasonixNativeOutput(nativeMessageBody(message), nativeMessageSupplement(message), callId)
  }
  const nativeOutput = readNativeOutput(await readNativeMessageSnapshot(native, agent.id))
  expect(nativeOutput.excerpt).not.toContain(output.omittedMarker)
  expect(nativeOutput.text).toContain(output.text)
  for (const value of [output.firstMarker, output.omittedMarker, output.lastMarker])
    expect(nativeOutput.text).toContain(value)
  const digest = createHash('sha256').update(nativeOutput.text).digest('hex')
  await testInfo.attach('reasonix-native-result-reference', { body: JSON.stringify({ agentId: agent.id, sessionId: agent.agentSessionId, callId, excerpt: nativeOutput.excerpt, previewText: nativeOutput.text, bytes: Buffer.byteLength(nativeOutput.text), sha256: digest }), contentType: 'application/json' })
  await proveNativeToolOutputFilePaths({
    context: native,
    callId,
    previewText: nativeOutput.text,
    paths: [],
    status: 'completed',
    previewMarkers: [output.firstMarker, output.lastMarker],
    prepareView: expandNativeResultView,
    workerProof: () => expectUnchangedNativeRecord(native, agent, readNativeOutput, nativeOutput),
  })
})

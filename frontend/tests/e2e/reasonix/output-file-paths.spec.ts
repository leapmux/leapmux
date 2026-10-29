import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { Buffer } from 'node:buffer'
import { createHash, randomUUID } from 'node:crypto'
import { readFileSync } from 'node:fs'
import process from 'node:process'
import { expect } from '@playwright/test'
import { MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeMessageBody, nativeMessageSupplement, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
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

for (const failed of [false, true]) {
  reasonixTest(`exposes the actual ${failed ? 'failed' : 'successful'} native output file and keeps the exact preview after reload`, async ({ native }, testInfo) => {
    await bypassToolRequests(native)
    const agent = await currentNativeAgent(native)
    const output = computedNativeToolOutput({ lineCount: 6000, padding: 48 })
    const program = `${output.source}\nprocess.stdout.write(completeOutput, () => { process.exitCode = ${failed ? 7 : 0}; });`
    const command = `${quotePosixShellArgument(process.execPath)} -e ${quotePosixShellArgument(program)}`
    expect(command).not.toContain(output.omittedMarker)
    const callId = `native-record-${randomUUID()}`
    await runNativeToolTurn(native, {
      toolCalls: [
        bashToolCall(native.provider, callId, command),
      ],
      prompt: 'Run the real large-output command once.',
      answer: 'The native record scenario ended.',
    })
    const readNativeOutput = (snapshot: NativeMessageSnapshot) => {
      const records = snapshot.messages.filter(message => message.source === MessageSource.AGENT
        && message.agentSessionId === agent.agentSessionId && message.spanId === callId)
        .filter((message) => {
          const body = nativeMessageBody(message)
          return typeof body === 'object' && body !== null && 'status' in body && body.status === (failed ? 'failed' : 'completed')
        })
      expect(records).toHaveLength(1)
      const message = records[0]
      if (!message)
        throw new Error('The native Reasonix result record is absent.')
      return reasonixNativeOutput(nativeMessageBody(message), nativeMessageSupplement(message), callId)
    }
    const nativeOutput = readNativeOutput(await readNativeMessageSnapshot(native, agent.id))
    expect(nativeOutput.excerpt).not.toContain(output.omittedMarker)
    const home = native.leapmuxServer.agentEnv?.HOME
    if (!home)
      throw new Error('The native Reasonix output proof requires its private home.')
    assertPrivateNativePath(nativeOutput.path, home)
    const saved = readFileSync(nativeOutput.path, 'utf8')
    expect(saved).toContain(output.text)
    for (const value of [output.firstMarker, output.omittedMarker, output.lastMarker])
      expect(saved).toContain(value)
    const digest = createHash('sha256').update(nativeOutput.text).digest('hex')
    await testInfo.attach('reasonix-native-result-reference', {
      body: JSON.stringify({
        agentId: agent.id,
        sessionId: agent.agentSessionId,
        callId,
        path: nativeOutput.path,
        outputBytes: Buffer.byteLength(saved),
        outputSHA256: createHash('sha256').update(saved).digest('hex'),
        excerpt: nativeOutput.excerpt,
        previewText: nativeOutput.previewText,
        text: nativeOutput.text,
        bytes: Buffer.byteLength(nativeOutput.text),
        sha256: digest,
      }),
      contentType: 'application/json',
    })
    await proveNativeToolOutputFilePaths({
      context: native,
      callId,
      previewText: nativeOutput.previewText,
      paths: [nativeOutput.path],
      status: failed ? 'failed' : 'completed',
      previewMarkers: [
        failed ? nativeOutput.previewText : output.firstMarker,
      ],
      absentMarkers: [output.omittedMarker, output.lastMarker],
      ...(failed
        ? {}
        : { prepareView: expandNativeResultView }),
      workerProof: () => expectUnchangedNativeRecord(native, agent, readNativeOutput, nativeOutput),
    })
  })
}

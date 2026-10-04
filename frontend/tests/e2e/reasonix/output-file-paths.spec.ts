import { Buffer } from 'node:buffer'
import { createHash, randomUUID } from 'node:crypto'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeMessageBody, nativeMessageSupplement, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { applyPermissionPreset, sendMessage } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'
import { reasonixNativeOutput } from './nativeToolOutput'

reasonixTest('keeps the native result record and exact inline Copy with no output file path after reload', async ({ authenticatedReasonixWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  await applyPermissionPreset(page, 'bypass')
  const agent = await currentNativeAgent(native)
  const output = computedNativeToolOutput({ lineCount: 6000, padding: 48 })
  const program = `${output.source}\nprocess.stdout.write(completeOutput);`
  const command = `${quotePosixShellArgument(process.execPath)} -e ${quotePosixShellArgument(program)}`
  expect(command).not.toContain(output.omittedMarker)
  const callId = `native-record-${randomUUID()}`
  await modelScript.queue({ toolCalls: [bashToolCall(native.provider, callId, command)] }, { text: 'The native record scenario ended.' })
  await sendMessage(page, modelScript.prompt('Run the real large-output command once.'))
  await waitForNativeToolSteps(native, 2)
  const readNativeOutput = async () => {
    const snapshot = await readNativeMessageSnapshot(native, agent.id)
    expect(snapshot.agentSessionId).toBe(agent.agentSessionId)
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
  const nativeOutput = await readNativeOutput()
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
    workerProof: async () => {
      expect(await readNativeOutput()).toEqual(nativeOutput)
    },
  })
})

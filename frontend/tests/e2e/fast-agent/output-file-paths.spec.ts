import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { readFileSync } from 'node:fs'
import { expect } from '@playwright/test'
import { fastAgentArchiveSource, sameFastAgentArchiveCall } from '../../../src/components/chat/providers/fastagent/archiveSource'
import { FAST_AGENT_SHELL } from '../../../src/components/chat/providers/fastagent/protocol'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { parseMessageContent } from '../../../src/lib/messageParser'
import { fastAgentTest } from '../fastagent-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent, expectNativeOptionValue, nativeOptionGroup } from '../helpers/nativeScenario'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput, nativeOutputFileCommand } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { fastAgentNativeBashToolCall } from '../helpers/providerToolCalls'
import { getGlobalState } from '../helpers/server'
import { createToolOutputControl } from '../helpers/toolOutputControl'
import { interruptButton, resumePausedQueue, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { fastAgentNativeOutput, fastAgentTerminalOutputFileLimit } from './nativeToolOutput'
import { enableNativeCommands } from './scenarios'

fastAgentTest('records the native client terminal output limit and retains its exact tail after reload', async ({ native }, testInfo) => {
  const output = computedNativeToolOutput({ lineCount: 6000, padding: 48 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'native-fast-agent-output-limit',
    proof: async (capture) => {
      const command = capture.call.arguments?.command
      if (typeof command !== 'string')
        throw new TypeError('The native Fast Agent output scenario has no exact command.')
      const frames = capture.snapshot.messages.filter(message => message.agentSessionId === capture.agent.agentSessionId).map(nativeMessageBody)
      const modelText = nativeToolResult(capture.request, capture.call.id)
      const limit = fastAgentTerminalOutputFileLimit(frames, command, modelText)
      await testInfo.attach('fast-agent-native-client-terminal-limit', {
        body: JSON.stringify({
          callId: limit.callId,
          modelCallId: capture.call.id,
          sessionId: capture.agent.agentSessionId,
          byteLimit: limit.byteLimit,
          previewText: limit.previewText,
          text: limit.text,
          modelText,
        }),
        contentType: 'application/json',
      })
      expect(limit.text).not.toContain(output.firstMarker)
      expect(limit.text).not.toContain(output.omittedMarker)
      expect(limit.text).toContain(output.lastMarker)
      const callRows = (snapshot: NativeMessageSnapshot) => snapshot.messages.filter(message => message.spanId === limit.callId)
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: limit.callId,
        previewText: limit.previewText,
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

for (const exitCode of [0, 7]) {
  fastAgentTest(`recovers native command output files and exit ${exitCode} from its root archive`, async ({ native }, testInfo) => {
    await expectNativeOptionValue(native, 'nativeCommands', 'false')
    expect(nativeOptionGroup(await currentNativeAgent(native), 'nativeCommands')?.mutable).toBe(false)
    await enableNativeCommands(native)
    const output = computedNativeToolOutput({ lineCount: 6000, padding: 48 })
    await captureNativeToolOutput(native, testInfo, {
      output,
      callId: `native-fast-agent-own-output-${exitCode}`,
      call: (output, id) => fastAgentNativeBashToolCall(id, nativeOutputFileCommand(output, { exitCode })),
      proof: async (capture) => {
        const readReceipt = (snapshot: NativeMessageSnapshot) => {
          const sources = snapshot.messages.filter(message => message.agentSessionId === capture.agent.agentSessionId).map(message => ({
            message,
            parsed: parseMessageContent(message),
            source: fastAgentArchiveSource(parseMessageContent(message)),
          }))
          const results = sources.filter(entry => entry.source?.kind === 'result' && entry.source.callId === capture.call.id)
          expect(results).toHaveLength(1)
          const result = results[0]
          if (!result || result.source?.kind !== 'result' || !result.parsed.parentObject)
            throw new Error('The native Fast Agent root archive result is absent or incomplete.')
          const source = result.source
          const frame = result.parsed.parentObject
          const requests = sources.filter(entry => entry.source?.kind === 'request' && sameFastAgentArchiveCall(entry.source, source))
          expect(requests).toHaveLength(1)
          expect(source.request.args).toEqual(capture.call.arguments)
          expect(result.message.spanId).toBe(source.spanId)
          const metadata = pickObject(frame, '_meta')
          expect(pickObject(metadata, FAST_AGENT_SHELL.Metadata)?.[FAST_AGENT_SHELL.ExitCode]).toBe(exitCode)
          const blocks = frame.content
          const first = Array.isArray(blocks)
            ? blocks[0]
            : undefined
          if (!isObject(first) || first.type !== 'text' || typeof first.text !== 'string')
            throw new Error('The native Fast Agent result has no original text block.')
          const previewText = first.text
          const path = fastAgentNativeOutput(previewText)
          assertPrivateNativePath(path, getGlobalState().tmpDir)
          expect(readFileSync(path, 'utf8')).toBe(output.text)
          expect(nativeToolResult(capture.request, capture.call.id)).toContain(previewText)
          expect(previewText).toContain(output.firstMarker)
          expect(previewText).toContain(output.lastMarker)
          expect(previewText).not.toContain(output.omittedMarker)
          return {
            frame,
            source,
            callId: source.spanId,
            previewText,
            paths: [path],
          }
        }
        const receipt = readReceipt(capture.snapshot)
        await testInfo.attach('fast-agent-native-root-archive-result', {
          body: JSON.stringify(receipt),
          contentType: 'application/json',
        })
        await proveNativeToolOutputFilePaths({
          context: native,
          callId: receipt.callId,
          previewText: receipt.previewText,
          previewMarkers: [output.firstMarker, output.lastMarker],
          absentMarkers: [output.omittedMarker],
          paths: receipt.paths,
          status: exitCode === 0
            ? 'completed'
            : 'failed',
          prepareView: expandNativeResultView,
          workerProof: () => expectUnchangedNativeRecord(native, capture.agent, readReceipt, receipt),
        })
      },
    })
  })
}

fastAgentTest('keeps interrupted native output with its original turn', async ({ native }, testInfo) => {
  await enableNativeCommands(native)
  const owner = await currentNativeAgent(native)
  const control = createToolOutputControl(owner.workingDir)
  const callId = 'native-fast-agent-interrupted-archive'
  await withCleanup(async () => {
    const start = await native.modelScript.queue({
      toolCalls: [
        fastAgentNativeBashToolCall(callId, control.command),
      ],
    })
    await sendMessage(native.page, native.modelScript.prompt('Run the native command until interruption.'))
    await native.modelScript.waitForSteps(start + 1)
    await control.waitForFirstOutput()
    await interruptButton(native.page).click()
    await waitForAgentIdle(native.page)
    const readReceipt = (snapshot: NativeMessageSnapshot) => {
      const rows = snapshot.messages.filter(message => message.agentSessionId === owner.agentSessionId)
      const sources = rows.map(message => ({
        message,
        source: fastAgentArchiveSource(parseMessageContent(message)),
      }))
      const results = sources.filter(entry => entry.source?.kind === 'result' && entry.source.callId === callId)
      expect(results).toHaveLength(1)
      const result = results[0]
      if (!result || result.source?.kind !== 'result')
        throw new Error('The interrupted Fast Agent native archive result is absent.')
      const source = result.source
      const requests = sources.filter(entry => entry.source?.kind === 'request' && sameFastAgentArchiveCall(entry.source, source))
      expect(requests).toHaveLength(1)
      expect(source.request.args.command).toBe(control.command)
      expect(source.result).toMatchObject({
        isError: true,
        content: [
          { type: 'text', text: '**The user interrupted this tool call**' },
        ],
      })
      expect(result.message.spanId).toBe(source.spanId)
      return { frame: source.result, callId: source.spanId, previewText: '**The user interrupted this tool call**' }
    }
    const receipt = readReceipt(await readNativeMessageSnapshot(native, owner.id))
    await expect(userBubbles(native.page)).toHaveCount(1)
    await testInfo.attach('fast-agent-interrupted-native-archive', {
      body: JSON.stringify(receipt),
      contentType: 'application/json',
    })
    await proveNativeToolOutputFilePaths({
      context: native,
      callId: receipt.callId,
      previewText: receipt.previewText,
      previewMarkers: ['The user interrupted this tool call'],
      paths: [],
      status: 'cancelled',
      workerProof: () => expectUnchangedNativeRecord(native, owner, readReceipt, receipt),
    })
    await expect(userBubbles(native.page)).toHaveCount(1)
    await resumePausedQueue(native.page)
    await sendNativeAnswer(native, 'Reply once after the interrupted native command.', 'The native session remains usable after interruption.')
    await expectUnchangedNativeRecord(native, owner, readReceipt, receipt)
  }, async () => {
    await control.releaseFirstOutput()
    await control.releaseFinalOutput()
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/527.

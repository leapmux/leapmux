import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { readFileSync } from 'node:fs'
import { expect } from '@playwright/test'
import { FASTAGENT_ARCHIVE } from '../../../src/generated/contracts/fastagent-protocol'
import { isObject } from '../../../src/lib/jsonPick'
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
import { interruptButton, resumePausedQueue, sendMessage, waitForAgentIdle } from '../helpers/ui'
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
        throw new Error('The native Fast Agent output scenario has no exact command.')
      const frames = capture.snapshot.messages.filter(message => message.agentSessionId === capture.agent.agentSessionId).map(nativeMessageBody)
      const modelText = nativeToolResult(capture.request, capture.call.id)
      const limit = fastAgentTerminalOutputFileLimit(frames, command, modelText)
      await testInfo.attach('fast-agent-native-client-terminal-limit', { body: JSON.stringify({ callId: limit.callId, modelCallId: capture.call.id, sessionId: capture.agent.agentSessionId, byteLimit: limit.byteLimit, previewText: limit.previewText, text: limit.text, modelText }), contentType: 'application/json' })
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
          const frames = snapshot.messages.filter(message => message.agentSessionId === capture.agent.agentSessionId).map(nativeMessageBody)
          const results = frames.filter(isObject).filter((frame) => {
            const meta = isObject(frame._meta) ? frame._meta[FASTAGENT_ARCHIVE.Namespace] : undefined
            return frame.sessionUpdate === 'tool_call_update' && isObject(meta)
              && meta[FASTAGENT_ARCHIVE.SessionID] === capture.agent.agentSessionId
              && meta[FASTAGENT_ARCHIVE.CallID] === capture.call.id
          })
          expect(results).toHaveLength(1)
          const frame = results[0]
          if (!frame || typeof frame.toolCallId !== 'string' || !isObject(frame.rawOutput)
            || !Array.isArray(frame.rawOutput.content) || !isObject(frame.rawOutput.content[0]) || typeof frame.rawOutput.content[0].text !== 'string') {
            throw new Error('The native Fast Agent root archive result is absent or incomplete.')
          }
          const requests = frames.filter(isObject).filter(request => request.sessionUpdate === 'tool_call' && request.toolCallId === frame.toolCallId)
          expect(requests).toHaveLength(1)
          expect(requests[0]?.rawInput).toEqual(capture.call.arguments)
          expect(frame.status).toBe(exitCode === 0 ? 'completed' : 'failed')
          const previewText = frame.rawOutput.content[0].text
          const path = fastAgentNativeOutput(previewText)
          assertPrivateNativePath(path, getGlobalState().tmpDir)
          expect(readFileSync(path, 'utf8')).toBe(output.text)
          expect(nativeToolResult(capture.request, capture.call.id)).toContain(previewText)
          expect(previewText).toContain(output.firstMarker)
          expect(previewText).toContain(output.lastMarker)
          expect(previewText).not.toContain(output.omittedMarker)
          return { frame, callId: frame.toolCallId, previewText, paths: [path] }
        }
        const receipt = readReceipt(capture.snapshot)
        await testInfo.attach('fast-agent-native-root-archive-result', { body: JSON.stringify(receipt), contentType: 'application/json' })
        await proveNativeToolOutputFilePaths({
          context: native,
          callId: receipt.callId,
          previewText: receipt.previewText,
          previewMarkers: [output.firstMarker, output.lastMarker],
          absentMarkers: [output.omittedMarker],
          paths: receipt.paths,
          status: exitCode === 0 ? 'completed' : 'failed',
          prepareView: expandNativeResultView,
          workerProof: () => expectUnchangedNativeRecord(native, capture.agent, readReceipt, receipt),
        })
      },
    })
  })
}

fastAgentTest('keeps the interrupted native archive result with its original turn', async ({ native }, testInfo) => {
  await enableNativeCommands(native)
  const owner = await currentNativeAgent(native)
  const control = createToolOutputControl(owner.workingDir)
  const callId = 'native-fast-agent-interrupted-archive'
  await withCleanup(async () => {
    const start = await native.modelScript.queue({ toolCalls: [fastAgentNativeBashToolCall(callId, control.command)] })
    await sendMessage(native.page, native.modelScript.prompt('Run the native command until interruption.'))
    await native.modelScript.waitForSteps(start + 1)
    await control.waitForFirstOutput()
    await interruptButton(native.page).click()
    await waitForAgentIdle(native.page)
    const readReceipt = (snapshot: NativeMessageSnapshot) => {
      const results = snapshot.messages.filter(message => message.agentSessionId === owner.agentSessionId).map(nativeMessageBody).filter(isObject).filter((frame) => {
        const archive = isObject(frame._meta) ? frame._meta[FASTAGENT_ARCHIVE.Namespace] : undefined
        return frame.sessionUpdate === 'tool_call_update' && isObject(archive) && archive[FASTAGENT_ARCHIVE.CallID] === callId
      })
      expect(results).toHaveLength(1)
      const frame = results[0]
      if (!frame || typeof frame.toolCallId !== 'string' || !isObject(frame.rawOutput) || !Array.isArray(frame.rawOutput.content))
        throw new Error('The interrupted Fast Agent native archive result is absent.')
      expect(frame.status).toBe('failed')
      expect(frame.rawOutput.isError).toBe(true)
      expect(frame.rawOutput.content).toEqual([{ type: 'text', text: '**The user interrupted this tool call**' }])
      return { frame, callId: frame.toolCallId, previewText: '**The user interrupted this tool call**' }
    }
    const receipt = readReceipt(await readNativeMessageSnapshot(native, owner.id))
    await testInfo.attach('fast-agent-interrupted-native-archive', { body: JSON.stringify(receipt), contentType: 'application/json' })
    await proveNativeToolOutputFilePaths({
      context: native,
      callId: receipt.callId,
      previewText: receipt.previewText,
      previewMarkers: ['The user interrupted this tool call'],
      paths: [],
      status: 'cancelled',
      workerProof: () => expectUnchangedNativeRecord(native, owner, readReceipt, receipt),
    })
    await resumePausedQueue(native.page)
    await sendNativeAnswer(native, 'Reply once after the interrupted native command.', 'The native session remains usable after interruption.')
    await expectUnchangedNativeRecord(native, owner, readReceipt, receipt)
  }, async () => {
    await control.releaseFirstOutput()
    await control.releaseFinalOutput()
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/527.

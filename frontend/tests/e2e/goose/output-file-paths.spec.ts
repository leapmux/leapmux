import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { readFileSync } from 'node:fs'
import { expect } from '@playwright/test'
import { MESSAGE_SUPPLEMENT_FIELD } from '../../../src/generated/contracts/worker-vocab'
import { isObject } from '../../../src/lib/jsonPick'
import { gooseTest } from '../goose-fixtures'
import { acpClosedToolCall, requireAcpToolSupplement } from '../helpers/acpToolFrame'
import { nativeMessageBody, nativeMessageSupplement } from '../helpers/nativeMessages'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { expectNativeOptionValue } from '../helpers/nativeScenario'
import { waitForNativeOptionApplied } from '../helpers/nativeSettings'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { getGlobalState } from '../helpers/server'
import { chooseSettingsOption } from '../helpers/ui'
import { bypassToolRequests } from './scenarios'
import { gooseTerminalOutputFileLimit } from './terminalOutputLimit'

gooseTest('records the native client terminal output limit and retains its exact output after reload', async ({ native }, testInfo) => {
  const output = computedNativeToolOutput({ lineCount: 6000, padding: 48 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'native-goose-output-limit',
    prepare: () => bypassToolRequests(native),
    proof: async (capture) => {
      const readLimit = (snapshot: NativeMessageSnapshot) => {
        const records = snapshot.messages.filter(message => message.agentSessionId === capture.agent.agentSessionId)
          .filter((message) => {
            const body = nativeMessageBody(message)
            return isObject(body) && acpClosedToolCall(body, capture.call.id, ['completed'])
          })
        expect(records).toHaveLength(1)
        const record = records[0]
        if (!record)
          throw new Error('The exact native Goose terminal result is absent.')
        return gooseTerminalOutputFileLimit(nativeMessageBody(record), nativeMessageSupplement(record), capture.call.id)
      }
      const limit = readLimit(capture.snapshot)
      const modelText = nativeToolResult(capture.request, capture.call.id)
      expect(modelText).toContain(limit.text)
      expect(modelText).not.toContain(output.omittedMarker)
      expect(limit.text).not.toContain(output.firstMarker)
      expect(limit.text).not.toContain(output.omittedMarker)
      expect(limit.text).toContain(output.lastMarker)
      await testInfo.attach('goose-native-client-terminal-limit', {
        body: JSON.stringify({ sessionId: capture.agent.agentSessionId, previewText: limit.text, ...limit }),
        contentType: 'application/json',
      })
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: capture.call.id,
        previewText: limit.text,
        previewMarkers: [output.lastMarker],
        absentMarkers: [output.omittedMarker],
        paths: [],
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: () => expectUnchangedNativeRecord(native, capture.agent, readLimit, limit),
      })
    },
  })
})

gooseTest('states its native output file paths when the agent runs commands itself', async ({ native }, testInfo) => {
  await expectNativeOptionValue(native, 'nativeCommands', 'false')
  await chooseSettingsOption(native.page, 'nativeCommands-true')
  await waitForNativeOptionApplied(native, 'nativeCommands', 'true')
  const output = computedNativeToolOutput({ lineCount: 6000, padding: 48 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'native-goose-own-output',
    prepare: () => bypassToolRequests(native),
    proof: async (capture) => {
      const readReceipt = (snapshot: NativeMessageSnapshot) => {
        const messages = snapshot.messages.filter(message => message.agentSessionId === capture.agent.agentSessionId)
          .filter((message) => {
            const frame = nativeMessageBody(message)
            return isObject(frame) && acpClosedToolCall(frame, capture.call.id, ['completed'])
          })
        expect(messages).toHaveLength(1)
        const message = messages[0]
        if (!message)
          throw new Error('The native Goose command result is absent.')
        const frame = nativeMessageBody(message)
        const supplement = nativeMessageSupplement(message)
        if (!isObject(frame) || !isObject(supplement))
          throw new Error('The native Goose command result has no retained protocol record.')
        const provider = requireAcpToolSupplement(frame, supplement[MESSAGE_SUPPLEMENT_FIELD.Provider], 'Goose output files')
        const raw = frame.rawOutput
        if (!isObject(raw) || typeof raw.stdout !== 'string' || raw.stderr !== '' || raw.exit_code !== 0)
          throw new Error('The native Goose command result has another output or exit state.')
        const texts = Array.isArray(frame.content)
          ? frame.content.filter(isObject).map(block => isObject(block.content)
              ? block.content.text
              : undefined).filter((text): text is string => typeof text === 'string')
          : []
        const paths = texts.flatMap(text => [
          ...text.matchAll(/Full output saved to (.+?)\. Read it with /gu),
        ].map(match => match[1]!))
        expect(paths).toHaveLength(2)
        expect(paths.map(path => path.split('/').at(-1))).toEqual(['stdout-0', 'output-0'])
        const modelText = nativeToolResult(capture.request, capture.call.id)
        for (const path of paths) {
          expect(modelText).toContain(path)
          assertPrivateNativePath(path, getGlobalState().tmpDir)
          expect(readFileSync(path, 'utf8').trimEnd()).toBe(output.text)
        }
        expect(raw.stdout).toContain(output.lastMarker)
        expect(raw.stdout).not.toContain(output.omittedMarker)
        return {
          frame,
          provider,
          paths,
          previewText: raw.stdout,
        }
      }
      const receipt = readReceipt(capture.snapshot)
      await testInfo.attach('goose-native-output-files', {
        body: JSON.stringify(receipt),
        contentType: 'application/json',
      })
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: capture.call.id,
        previewText: receipt.previewText,
        previewMarkers: [output.lastMarker],
        absentMarkers: [output.omittedMarker],
        paths: receipt.paths,
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: () => expectUnchangedNativeRecord(native, capture.agent, readReceipt, receipt),
      })
    },
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/527.

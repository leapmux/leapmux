import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { existsSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { ampTest, expect } from '../amp-fixtures'
import { ampToolUseID } from '../helpers/ampSurface'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { nativeMessageBody } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { getGlobalState } from '../helpers/server'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { ampNativeOutputLimit } from './nativeToolOutput'
import { ampToolResult } from './toolResult'

ampTest('records the native large shell output limit and retains its exact tail after reload', async ({ native }, testInfo) => {
  const before = await currentNativeAgent(native)
  if (!before.workingDir)
    throw new Error('The Amp native output proof requires a native working directory.')
  const sourceFile = join(before.workingDir, 'amp-controlled-complete-output.txt')
  assertPrivateNativePath(before.workingDir, getGlobalState().tmpDir)
  expect(existsSync(sourceFile)).toBe(false)
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'amp-native-output-limit',
    call: (sourceOutput, callId) => {
      const source = `${sourceOutput.source} require("node:fs").writeFileSync(${JSON.stringify(sourceFile)}, completeOutput, { flag: "wx" }); process.stdout.write(completeOutput)`
      return bashToolCall(native.provider, callId, `node -e ${quotePosixShellArgument(source)}`)
    },
    proof: async (capture) => {
      const nativeId = ampToolUseID(capture.agent.agentSessionId, capture.call.id)
      expect(capture.agent.id).toBe(before.id)
      expect(capture.agent.workingDir).toBe(before.workingDir)
      expect(dirname(sourceFile)).toBe(capture.agent.workingDir)
      expect(existsSync(sourceFile)).toBe(true)
      assertPrivateNativePath(sourceFile, getGlobalState().tmpDir)
      const exact = ampToolResult(capture.request, nativeId)
      expect(exact.exitCode).toBe(0)
      const retained = ampNativeOutputLimit(exact.text, output.text)
      await testInfo.attach('amp-native-output-limit', { body: JSON.stringify({ nativeId, sessionId: capture.agent.agentSessionId, sourceFile, previewText: retained.output, retained }, null, 2), contentType: 'application/json' })
      if ('prefixLinesOmitted' in retained)
        expect(retained.prefixLinesOmitted).toBeGreaterThan(0)
      else
        expect(retained.exitCode).toBe(0)
      expect(retained.output).toContain(output.lastMarker)
      expect(retained.output).not.toContain(output.firstMarker)
      expect(retained.output).not.toContain(output.omittedMarker)
      expect(retained.output).not.toContain(output.text)
      const callFrames = (snapshot: NativeMessageSnapshot) => snapshot.messages.filter(message => message.spanId === nativeId).map(nativeMessageBody)
      const originals = callFrames(capture.snapshot)
      expect(originals).not.toHaveLength(0)
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: nativeId,
        previewText: retained.output,
        previewMarkers: [output.lastMarker],
        // Amp keeps only the tail, so the row must show neither the first line nor the middle line.
        absentMarkers: [output.firstMarker, output.omittedMarker],
        paths: [],
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: () => expectUnchangedNativeRecord(native, capture.agent, callFrames, originals),
      })
    },
  })
})

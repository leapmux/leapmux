import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { diracTest, expect } from '../dirac-fixtures'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codeExecutionToolCall } from '../helpers/providerToolCalls'
import { diracScriptReceipt } from './codeExecution'
import { diracNativeOutputPaths, diracOutputFileFrames } from './outputFilePaths'
import { nativeContext } from './scenarios'

diracTest('keeps the native script limit and its retained preview after reload', async ({ authenticatedDiracWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  const source = `${output.source}\nprocess.stdout.write(completeOutput)`
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'dirac-native-script-limit',
    call: (_output, callId) => codeExecutionToolCall(AgentProvider.DIRAC, callId, source),
    proof: async (capture) => {
      const frames = diracOutputFileFrames(capture.snapshot)
      const receipt = diracScriptReceipt(frames, source)
      await testInfo.attach('dirac-native-script-preview', { body: JSON.stringify({ sessionId: capture.agent.agentSessionId, receipt }, null, 2), contentType: 'application/json' })
      expect(receipt.exitCode).toBe(0)
      expect(receipt.failed).toBe(false)
      expect(receipt.output).toContain(output.firstMarker)
      expect(receipt.output).toContain(output.lastMarker)
      expect(receipt.output).not.toContain(output.omittedMarker)
      expect(receipt.output).toMatch(/truncated|omitted/i)
      const temporaryDir = leapmuxServer.agentEnv.TMPDIR
      if (!temporaryDir)
        throw new Error('The native Dirac case requires its private runtime directory.')
      const pointer = diracNativeOutputPaths(frames, source, temporaryDir)
      expect(pointer.callId).toBe(receipt.callId)
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: pointer.callId,
        previewText: pointer.previewText,
        previewMarkers: [output.firstMarker, output.lastMarker],
        paths: pointer.paths,
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: async () => {
          const current = await currentNativeAgent(native)
          expect(current.id).toBe(capture.agent.id)
          expect(current.agentSessionId).toBe(capture.agent.agentSessionId)
          const snapshot = await readNativeMessageSnapshot(native, current.id)
          const storedFrames = diracOutputFileFrames(snapshot)
          expect(diracScriptReceipt(storedFrames, source)).toEqual(receipt)
          expect(diracNativeOutputPaths(storedFrames, source, temporaryDir)).toEqual(pointer)
          expect(storedFrames).toEqual(frames)
        },
      })
    },
  })
})

diracTest('keeps the generated native log path and exact preview Copy after reload', async ({ authenticatedDiracWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  const temporaryDir = leapmuxServer.agentEnv.TMPDIR
  if (!temporaryDir || leapmuxServer.agentEnv.TEMP !== temporaryDir || leapmuxServer.agentEnv.TMP !== temporaryDir)
    throw new Error('The native Dirac output requires one isolated runtime temp directory.')
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  const source = `${output.source}\nprocess.stdout.write(completeOutput)`
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'dirac-native-log-path',
    call: (_output, callId) => codeExecutionToolCall(AgentProvider.DIRAC, callId, source),
    nativeCallId: (_request, _scriptedId, snapshot) => diracScriptReceipt(diracOutputFileFrames(snapshot), source).callId,
    proof: async (capture) => {
      const frames = diracOutputFileFrames(capture.snapshot)
      const receipt = diracNativeOutputPaths(frames, source, temporaryDir)
      expect(receipt.callId).toBe(capture.nativeCallId)
      expect(receipt.exitCode).toBe(0)
      expect(receipt.failed).toBe(false)
      expect(receipt.previewText).toContain(output.firstMarker)
      expect(receipt.previewText).toContain(output.lastMarker)
      expect(receipt.previewText).not.toContain(output.omittedMarker)
      expect(receipt.previewText).toMatch(/truncated|omitted/i)
      assertPrivateNativePath(receipt.paths[0]!, temporaryDir)
      expect(nativeToolResult(capture.request, capture.call.id)).toContain(receipt.paths[0]!)
      await testInfo.attach('dirac-native-output-path-receipt', { body: JSON.stringify({ agentId: capture.agent.id, sessionId: capture.agent.agentSessionId, receipt, frames }), contentType: 'application/json' })
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: capture.nativeCallId,
        previewText: receipt.previewText,
        previewMarkers: [output.firstMarker, output.lastMarker],
        paths: receipt.paths,
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: async () => {
          const current = await currentNativeAgent(native)
          expect(current.id).toBe(capture.agent.id)
          expect(current.agentSessionId).toBe(capture.agent.agentSessionId)
          const snapshot = await readNativeMessageSnapshot(native, current.id)
          const storedFrames = diracOutputFileFrames(snapshot)
          expect(diracNativeOutputPaths(storedFrames, source, temporaryDir)).toEqual(receipt)
          expect(storedFrames).toEqual(frames)
        },
      })
    },
  })
})

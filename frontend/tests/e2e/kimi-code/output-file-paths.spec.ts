import type { TestInfo } from '@playwright/test'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeToolOutput } from '../helpers/nativeToolOutput'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { getGlobalState } from '../helpers/server'
import { applyPermissionPreset } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'
import { KIMI_OUTPUT_PATH_CALL_IDS, kimiNativeOutputPointer, readKimiNativeOutput } from './outputFilePaths'
import { computedKimiPerLineOutputFileOutput } from './perLineOutput'

async function proveKimiOutputFilePaths(options: {
  native: ManagedNativeScenarioContext
  testInfo: TestInfo
  output: NativeToolOutput
  format: 'header' | 'per-line' | 'mcp'
}): Promise<void> {
  const { native, testInfo, format } = options
  await native.page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  await applyPermissionPreset(native.page, 'bypass')
  await captureNativeToolOutput(native, testInfo, {
    output: options.output,
    callId: format === 'header' ? KIMI_OUTPUT_PATH_CALL_IDS.Header : format === 'per-line' ? KIMI_OUTPUT_PATH_CALL_IDS.PerLine : KIMI_OUTPUT_PATH_CALL_IDS.AmbiguousMcp,
    ...(format === 'mcp' ? { call: (output: NativeToolOutput, id: string) => mcpToolCall(AgentProvider.KIMI_CODE, id, { server: 'echo_probe', tool: 'echo', input: { value: output.text } }) } : {}),
    proof: async (capture) => {
      const receipt = readKimiNativeOutput(capture.snapshot, capture.nativeCallId, capture.call.name)
      const projected = nativeToolResult(capture.request, capture.nativeCallId)
      const modelPointer = kimiNativeOutputPointer(projected, capture.nativeCallId, capture.call.name)
      expect(modelPointer.path).toBe(receipt.path)
      assertPrivateNativePath(receipt.path, getGlobalState().tmpDir)
      expect(projected).not.toContain(capture.output.omittedMarker)
      expect(receipt.previewText).not.toContain(capture.output.omittedMarker)
      if (format === 'per-line')
        expect(projected).toContain('[Per-line truncation occurred; the complete output was saved to a file.')
      else
        expect(projected).toContain('Tool output exceeded 50000 characters; the full output was saved to a file.')
      const previewMarkers = [capture.output.firstMarker, capture.output.lastMarker].filter(marker => receipt.previewText.includes(marker))
      expect(previewMarkers.length).toBeGreaterThan(0)
      await testInfo.attach('kimi-native-output-path-receipt', { body: JSON.stringify({ agentId: capture.agent.id, sessionId: capture.agent.agentSessionId, callId: capture.nativeCallId, path: receipt.path, modelPointer, previewText: receipt.previewText, frame: receipt.frame }), contentType: 'application/json' })
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: capture.nativeCallId,
        previewText: receipt.previewText,
        previewMarkers,
        paths: receipt.paths,
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: async () => {
          const snapshot = await readNativeMessageSnapshot(native, capture.agent.id)
          expect(snapshot.agentId).toBe(capture.agent.id)
          expect(snapshot.agentSessionId).toBe(capture.agent.agentSessionId)
          const current = readKimiNativeOutput(snapshot, capture.nativeCallId, capture.call.name)
          expect(current.frame).toEqual(receipt.frame)
          expect(current.content).toEqual(receipt.content)
          expect(current.paths).toEqual(receipt.paths)
          expect(current.previewText).toBe(receipt.previewText)
        },
      })
    },
  })
}

kimiTest('keeps the native output header path and exact preview after reload', async ({ authenticatedKimiWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  await proveKimiOutputFilePaths({
    native: { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE },
    testInfo,
    output: computedNativeToolOutput({ lineCount: 6000, padding: 48 }),
    format: 'header',
  })
})

kimiTest('keeps the native per-line output path and exact preview after reload', async ({ authenticatedKimiWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  await proveKimiOutputFilePaths({
    native: { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE },
    testInfo,
    output: computedKimiPerLineOutputFileOutput(),
    format: 'per-line',
  })
})

kimiTest('keeps the native MCP output path and its original preview after reload', async ({ authenticatedKimiWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  await proveKimiOutputFilePaths({
    native: { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE },
    testInfo,
    output: computedNativeToolOutput({ lineCount: 3000, padding: 30 }),
    format: 'mcp',
  })
})

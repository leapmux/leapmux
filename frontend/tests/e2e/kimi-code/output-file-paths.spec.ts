import type { TestInfo } from '@playwright/test'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeToolOutput } from '../helpers/nativeToolOutput'
import { expect } from '@playwright/test'
import { MCP_ECHO_SERVER_NAME } from '../helpers/mcpEchoServer'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
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
  await applyPermissionPreset(native.page, 'bypass')
  await captureNativeToolOutput(native, testInfo, {
    output: options.output,
    callId: format === 'header' ? KIMI_OUTPUT_PATH_CALL_IDS.Header : format === 'per-line' ? KIMI_OUTPUT_PATH_CALL_IDS.PerLine : KIMI_OUTPUT_PATH_CALL_IDS.AmbiguousMcp,
    ...(format === 'mcp' ? { call: (output: NativeToolOutput, id: string) => mcpToolCall(native.provider, id, { server: MCP_ECHO_SERVER_NAME, tool: 'echo', input: { value: output.text } }) } : {}),
    // Kimi's reader also checks the tool name that its pointer header states.
    proof: capture => proveNativeOutputReceipt(capture, testInfo, (snapshot, callId) => readKimiNativeOutput(snapshot, callId, capture.call.name), {
      // The MCP echo call takes the complete output as its argument, and the result row draws that argument.
      argumentsHoldOutput: format === 'mcp',
      extraProof: (receipt) => {
        const projected = nativeToolResult(capture.request, capture.nativeCallId)
        expect(kimiNativeOutputPointer(projected, capture.nativeCallId, capture.call.name).path).toBe(receipt.path)
        expect(projected).not.toContain(capture.output.omittedMarker)
        if (format === 'per-line')
          expect(projected).toContain('[Per-line truncation occurred; the complete output was saved to a file.')
        else
          expect(projected).toContain('Tool output exceeded 50000 characters; the full output was saved to a file.')
      },
    }),
  })
}

kimiTest('keeps the native output header path and exact preview after reload', async ({ native }, testInfo) => {
  await proveKimiOutputFilePaths({ native, testInfo, output: computedNativeToolOutput({ lineCount: 6000, padding: 48 }), format: 'header' })
})

kimiTest('keeps the native per-line output path and exact preview after reload', async ({ native }, testInfo) => {
  await proveKimiOutputFilePaths({ native, testInfo, output: computedKimiPerLineOutputFileOutput(), format: 'per-line' })
})

kimiTest('keeps the native MCP output path and its original preview after reload', async ({ native }, testInfo) => {
  await proveKimiOutputFilePaths({ native, testInfo, output: computedNativeToolOutput({ lineCount: 3000, padding: 30 }), format: 'mcp' })
})

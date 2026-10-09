import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { basename, dirname } from 'node:path'
import { expect } from '@playwright/test'
import { KIRO_OPTION, KIRO_POLICY_PRESET } from '../../../src/generated/contracts/kiro-protocol'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { acpClosedToolCall } from '../helpers/acpToolFrame'
import { nativeMessageBody } from '../helpers/nativeMessages'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { computedNativeToolOutput, copyNativeToolOutputPreview } from '../helpers/nativeToolOutput'
import { proveNativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { openWorkspace, toolCallRow } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { kiroTest } from '../kiro-fixtures'
import { readKiroNativeOutput } from './outputFilePaths'
import { KIRO_AGENT, nativeContext } from './scenarios'
import { readKiroToolSupplement } from './toolRecord'
import { kiroToolResult } from './toolResult'

function nativeKiroResult(snapshot: NativeMessageSnapshot, callId: string) {
  const records = snapshot.messages.filter(message => message.agentSessionId === snapshot.agentSessionId && message.spanId === callId)
    .map(message => ({ original: nativeMessageBody(message), supplement: readKiroToolSupplement(message) }))
    .filter(record => isObject(record.original) && acpClosedToolCall(record.original, callId, ['completed']))
  const record = records[0]
  if (records.length !== 1 || !record || !isObject(record.original))
    throw new Error('The Kiro output path proof requires one exact native completed result.')
  return { original: record.original, supplement: record.supplement ?? undefined }
}

/** Read the Kiro receipt and every original row of its call, so the Worker proof requires the rows unchanged too. */
function readKiroReceiptWithRows(snapshot: NativeMessageSnapshot, callId: string) {
  return { ...readKiroNativeOutput(snapshot, callId), rows: snapshot.messages.filter(message => message.spanId === callId).map(nativeMessageBody) }
}

kiroTest('keeps native shell output inline without an output file path', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openProviderAgent(leapmuxServer, context.workspaceId, KIRO_AGENT, { optionValues: { [KIRO_OPTION.PolicyPreset]: KIRO_POLICY_PRESET.AllowAll } })
  await openWorkspace(page, context.workspaceId)
  await captureNativeToolOutput(context, testInfo, {
    output: computedNativeToolOutput({ lineCount: 200, padding: 20 }),
    callId: 'native-inline-output-limit',
    nativeCallId: (_request, scriptedId) => `run_command_${scriptedId}`,
    proof: async ({ request, call, nativeCallId, output, snapshot }) => {
      const result = kiroToolResult(request, call.id)
      expect(result.failed).toBe(false)
      expect(result.exitCode).toBe(0)
      expect(result.text).toContain(output.omittedMarker)
      expect(result.text).toContain(output.lastMarker)
      expect(result.text).not.toContain('Full output:')
      expect(result.text).not.toContain('artifact://')
      const stored = nativeKiroResult(snapshot, nativeCallId)
      expect(pickObject(pickObject(stored.original, '_meta'), 'kiro')?.outputTransformation).toBeUndefined()
      const bubble = toolCallRow(page, nativeCallId)
      await expect(bubble).toHaveCount(1)
      await expect(bubble.getByTestId('tool-output-file-paths')).toHaveCount(0)
      await copyNativeToolOutputPreview(page, bubble, output.text)
      await page.reload()
      await openWorkspace(page, context.workspaceId)
      await expect(bubble.getByTestId('tool-output-file-paths')).toHaveCount(0)
      await copyNativeToolOutputPreview(page, bubble, output.text)
    },
  })
})

kiroTest('keeps the offloaded native shell path and exact preview after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openProviderAgent(leapmuxServer, context.workspaceId, KIRO_AGENT, { optionValues: { [KIRO_OPTION.PolicyPreset]: KIRO_POLICY_PRESET.AllowAll } })
  await openWorkspace(page, context.workspaceId)
  const home = leapmuxServer.agentEnv.HOME
  if (!home)
    throw new Error('The Kiro output path proof requires the isolated native HOME.')
  await captureNativeToolOutput(context, testInfo, {
    output: computedNativeToolOutput({ lineCount: 3000, padding: 30 }),
    callId: 'native-offloaded-output-path',
    nativeCallId: (_request, scriptedId) => `run_command_${scriptedId}`,
    proof: async (capture) => {
      const saved = nativeKiroResult(capture.snapshot, capture.nativeCallId)
      const reference = pickObject(pickObject(pickObject(saved.original, '_meta'), 'kiro'), 'outputTransformation')
      expect(reference?.kind).toBe('offloaded')
      if (typeof reference?.absFilePath !== 'string')
        throw new Error('The native Kiro result contains no output path.')
      const path = reference.absFilePath
      assertPrivateNativePath(path, home)
      expect(basename(dirname(dirname(path)))).toBe(capture.agent.agentSessionId)
      expect(basename(path)).toMatch(/^execute_bash-[a-f0-9]{8}\.txt$/u)
      const output = pickObject(saved.original, 'rawOutput')
      if (typeof output?.output !== 'string' || typeof output.message !== 'string')
        throw new Error('The Kiro result contains no native output preview and reference.')
      expect(output?.exitCode).toBe(0)
      expect(output?.output).not.toContain(capture.output.omittedMarker)
      expect(output?.message).toContain(path)
      const modelResult = kiroToolResult(capture.request, capture.call.id)
      expect(modelResult.failed).toBe(false)
      expect(modelResult.exitCode).toBe(0)
      expect(modelResult.text).toContain(path)
      expect(modelResult.text).not.toContain(capture.output.omittedMarker)
      // Kiro keeps its output file under the native HOME of the agent.
      await proveNativeOutputReceipt(capture, testInfo, readKiroReceiptWithRows, {
        privateRoot: home,
        extraProof: receipt => expect(receipt.paths).toEqual([path]),
      })
    },
  })
})

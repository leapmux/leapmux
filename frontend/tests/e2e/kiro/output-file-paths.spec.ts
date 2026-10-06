import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { basename, dirname } from 'node:path'
import { KIRO_OPTION, KIRO_POLICY_PRESET } from '../../../src/generated/contracts/kiro-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { kiroToolResult } from '../helpers/kiroToolResult'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput, copyNativeToolOutputPreview } from '../helpers/nativeToolOutput'
import { proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { expect, KIRO_AGENT, kiroTest } from '../kiro-fixtures'
import { readKiroNativeOutput } from './outputFilePaths'
import { readKiroToolSupplement } from './toolRecord'

function nativeKiroResult(snapshot: NativeMessageSnapshot, callId: string) {
  const records = snapshot.messages.filter(message => message.agentSessionId === snapshot.agentSessionId && message.spanId === callId)
    .map(message => ({ original: nativeMessageBody(message), supplement: readKiroToolSupplement(message) }))
    .filter(record => isObject(record.original) && record.original.sessionUpdate === 'tool_call_update' && record.original.toolCallId === callId && record.original.status === 'completed')
  const record = records[0]
  if (records.length !== 1 || !record || !isObject(record.original))
    throw new Error('The Kiro output path proof requires one exact native completed result.')
  return { original: record.original, supplement: record.supplement ?? undefined }
}

kiroTest('keeps native shell output inline without an output file path', async ({ authenticatedEmptyWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.KIRO }
  await openProviderAgent(leapmuxServer, native.workspaceId, KIRO_AGENT, { optionValues: { [KIRO_OPTION.PolicyPreset]: KIRO_POLICY_PRESET.AllowAll } })
  await openWorkspace(page, native.workspaceId)
  await captureNativeToolOutput(native, testInfo, {
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
      const bubble = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${nativeCallId}"][data-tool-row-role="result"]:visible`)
      await expect(bubble).toHaveCount(1)
      await expect(bubble.getByTestId('tool-output-file-paths')).toHaveCount(0)
      await copyNativeToolOutputPreview(page, bubble, output.text)
      await page.reload()
      await openWorkspace(page, native.workspaceId)
      await expect(bubble.getByTestId('tool-output-file-paths')).toHaveCount(0)
      await copyNativeToolOutputPreview(page, bubble, output.text)
    },
  })
})

kiroTest('keeps the offloaded native shell path and exact preview after reload', async ({ authenticatedEmptyWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.KIRO }
  await openProviderAgent(leapmuxServer, native.workspaceId, KIRO_AGENT, { optionValues: { [KIRO_OPTION.PolicyPreset]: KIRO_POLICY_PRESET.AllowAll } })
  await openWorkspace(page, native.workspaceId)
  const home = leapmuxServer.agentEnv.HOME
  if (!home)
    throw new Error('The Kiro output path proof requires the isolated native HOME.')
  await captureNativeToolOutput(native, testInfo, {
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
      const originalRows = capture.snapshot.messages.filter(message => message.spanId === capture.nativeCallId).map(nativeMessageBody)
      const receipt = readKiroNativeOutput(capture.snapshot, capture.nativeCallId)
      expect(receipt.paths).toEqual([path])
      const previewMarkers = [capture.output.firstMarker, capture.output.lastMarker].filter(marker => receipt.previewText.includes(marker))
      expect(previewMarkers.length).toBeGreaterThan(0)
      await testInfo.attach('kiro-native-output-path-receipt', { body: JSON.stringify({ agentId: capture.agent.id, sessionId: capture.agent.agentSessionId, callId: capture.nativeCallId, paths: receipt.paths, previewText: receipt.previewText, reference, frame: receipt.frame }), contentType: 'application/json' })
      await proveNativeToolOutputFilePaths({
        context: native,
        callId: capture.nativeCallId,
        previewText: receipt.previewText,
        previewMarkers,
        paths: receipt.paths,
        status: 'completed',
        prepareView: expandNativeResultView,
        workerProof: async () => {
          const current = await currentNativeAgent(native)
          expect(current.id).toBe(capture.agent.id)
          expect(current.agentSessionId).toBe(capture.agent.agentSessionId)
          const snapshot = await readNativeMessageSnapshot(native, current.id)
          expect(snapshot.messages.filter(message => message.spanId === capture.nativeCallId).map(nativeMessageBody)).toEqual(originalRows)
          const retained = readKiroNativeOutput(snapshot, capture.nativeCallId)
          expect(retained.frame).toEqual(receipt.frame)
          expect(retained.content).toEqual(receipt.content)
          expect(retained.paths).toEqual(receipt.paths)
          expect(retained.previewText).toBe(receipt.previewText)
        },
      })
    },
  })
})

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { clineTest, expect } from '../cline-fixtures'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput, copyNativeToolOutputPreview } from '../helpers/nativeToolOutput'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { openWorkspace } from '../helpers/ui'
import { clineNativeOutputLimit } from './nativeToolOutput'

clineTest('records the completed command output limit and retains its native head and tail after reload', async ({ authenticatedClineWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedClineWorkspace.workspaceId, provider: AgentProvider.CLINE }
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'cline-native-output-limit',
    proof: async (capture) => {
      const exact = nativeToolResult(capture.request, capture.nativeCallId)
      const frames = capture.snapshot.messages.filter(message => message.spanId === capture.nativeCallId).map(nativeMessageBody)
      const operations = frames.filter(frame => isObject(frame) && frame.event === 'tool.finished' && frame.sessionId === capture.agent.agentSessionId
        && isObject(frame.payload) && frame.payload.toolCallId === capture.nativeCallId && frame.payload.toolName === 'run_commands')
      expect(operations).toHaveLength(1)
      const operation = operations[0]
      if (!operation)
        throw new Error('The Cline result has no exact completed native command operation.')
      const retained = clineNativeOutputLimit(JSON.stringify(operation))
      await testInfo.attach('cline-native-output-limit', { body: JSON.stringify({ nativeId: capture.nativeCallId, sessionId: capture.agent.agentSessionId, nativeReference: operation, modelProjection: exact, previewText: retained, retained }, null, 2), contentType: 'application/json' })
      expect(retained).toContain(output.firstMarker)
      expect(retained).toContain(output.lastMarker)
      expect(retained).not.toContain(output.omittedMarker)
      const originals = capture.snapshot.messages.filter(message => message.spanId === capture.nativeCallId)
      const result = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${capture.nativeCallId}"][data-tool-row-role="result"]:visible`)
      for (const reload of [false, true]) {
        if (reload) {
          await page.reload()
          await openWorkspace(page, native.workspaceId)
        }
        const current = await currentNativeAgent(native)
        expect(current.id).toBe(capture.agent.id)
        expect(current.agentSessionId).toBe(capture.agent.agentSessionId)
        const stored = await readNativeMessageSnapshot(native, current.id)
        expect(stored.messages.filter(message => message.spanId === capture.nativeCallId)).toEqual(originals)
        await expect(result).toHaveCount(1)
        await expandNativeResultView(result)
        await expect(result).toContainText(output.firstMarker)
        await expect(result).toContainText(output.lastMarker)
        await expect(result).not.toContainText(output.omittedMarker)
        await expect(result.getByTestId('tool-output-file-paths')).toHaveCount(0)
        await copyNativeToolOutputPreview(page, result, retained)
      }
    },
  })
})

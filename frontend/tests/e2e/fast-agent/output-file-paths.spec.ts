import { expect, fastAgentTest } from '../fastagent-fixtures'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput, copyNativeToolOutputPreview } from '../helpers/nativeToolOutput'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { openWorkspace } from '../helpers/ui'
import { fastAgentTerminalOutputFileLimit } from './nativeToolOutput'
import { nativeContext } from './scenarios'

fastAgentTest('records the native client terminal output limit and retains its exact tail after reload', async ({ authenticatedFastAgentWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
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
      await testInfo.attach('fast-agent-native-client-terminal-limit', { body: JSON.stringify({ callId: limit.callId, modelCallId: capture.call.id, sessionId: capture.agent.agentSessionId, byteLimit: limit.byteLimit, previewText: limit.text, text: limit.text, modelText }), contentType: 'application/json' })
      expect(limit.text).not.toContain(output.firstMarker)
      expect(limit.text).not.toContain(output.omittedMarker)
      expect(limit.text).toContain(output.lastMarker)
      const originals = capture.snapshot.messages.filter(message => message.spanId === limit.callId)
      const result = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${limit.callId}"][data-tool-row-role="result"]:visible`)
      for (const reload of [false, true]) {
        if (reload) {
          await page.reload()
          await openWorkspace(page, native.workspaceId)
        }
        const current = await currentNativeAgent(native)
        expect(current.id).toBe(capture.agent.id)
        expect(current.agentSessionId).toBe(capture.agent.agentSessionId)
        const stored = await readNativeMessageSnapshot(native, current.id)
        expect(stored.messages.filter(message => message.spanId === limit.callId)).toEqual(originals)
        await expect(result).toHaveCount(1)
        await expect(result).toHaveAttribute('data-tool-status', 'completed')
        await expandNativeResultView(result)
        await expect(result).toContainText(output.lastMarker)
        await expect(result).not.toContainText(output.omittedMarker)
        await expect(result.getByTestId('tool-output-file-paths')).toHaveCount(0)
        await copyNativeToolOutputPreview(page, result, limit.text)
      }
    },
  })
})

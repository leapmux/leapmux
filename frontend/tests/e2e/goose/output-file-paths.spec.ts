import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, gooseTest } from '../goose-fixtures'
import { nativeMessageBody, nativeMessageSupplement, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput, copyNativeToolOutputPreview } from '../helpers/nativeToolOutput'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { applyPermissionPreset, openWorkspace } from '../helpers/ui'
import { gooseTerminalOutputFileLimit } from './terminalOutputLimit'

gooseTest('records the native client terminal output limit and retains its exact output after reload', async ({ authenticatedGooseWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedGooseWorkspace.workspaceId, provider: AgentProvider.GOOSE }
  const output = computedNativeToolOutput({ lineCount: 6000, padding: 48 })
  await captureNativeToolOutput(native, testInfo, {
    output,
    callId: 'native-goose-output-limit',
    prepare: () => applyPermissionPreset(page, 'bypass'),
    proof: async (capture) => {
      const readLimit = async () => {
        const snapshot = await readNativeMessageSnapshot(native, capture.agent.id)
        const records = snapshot.messages.filter(message => message.agentSessionId === capture.agent.agentSessionId)
          .filter((message) => {
            const body = nativeMessageBody(message)
            return typeof body === 'object' && body !== null && 'sessionUpdate' in body && body.sessionUpdate === 'tool_call_update'
              && 'toolCallId' in body && body.toolCallId === capture.call.id && 'status' in body && body.status === 'completed'
          })
        expect(records).toHaveLength(1)
        const record = records[0]
        if (!record)
          throw new Error('The exact native Goose terminal result is absent.')
        return gooseTerminalOutputFileLimit(nativeMessageBody(record), nativeMessageSupplement(record), capture.call.id)
      }
      const limit = await readLimit()
      const modelText = nativeToolResult(capture.request, capture.call.id)
      expect(modelText).toContain(limit.text)
      expect(modelText).not.toContain(output.omittedMarker)
      expect(limit.text).not.toContain(output.firstMarker)
      expect(limit.text).not.toContain(output.omittedMarker)
      expect(limit.text).toContain(output.lastMarker)
      await testInfo.attach('goose-native-client-terminal-limit', { body: JSON.stringify({ sessionId: capture.agent.agentSessionId, previewText: limit.text, ...limit }), contentType: 'application/json' })
      const result = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${capture.call.id}"][data-tool-row-role="result"]:visible`)
      for (const reload of [false, true]) {
        if (reload) {
          await page.reload()
          await openWorkspace(page, native.workspaceId)
        }
        const current = await currentNativeAgent(native)
        expect(current.id).toBe(capture.agent.id)
        expect(current.agentSessionId).toBe(capture.agent.agentSessionId)
        expect(await readLimit()).toEqual(limit)
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

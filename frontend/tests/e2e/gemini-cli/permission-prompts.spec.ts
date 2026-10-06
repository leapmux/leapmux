import { randomUUID } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { GEMINI_TOOL } from '../../../src/generated/contracts/gemini-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { geminiTest } from '../gemini-fixtures'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision, exerciseNativePermissionWrite, expectDeclinedToolRow } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResultContent } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { messageBubbles, openWorkspace } from '../helpers/ui'
import { nativeContext } from './scenarios'

geminiTest('requires a native permission decision before a real file change', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseNativePermissionWrite(context)
})

/**
 * Deny selects the ACP option `cancel`.
 * Gemini CLI then fails the tool and sends the refusal to the model as the function response of the same call.
 * The agent loop continues with that response, so the next model request carries it.
 */
geminiTest('sends the exact native refusal after a Deny decision and keeps the file bytes', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  const fileName = 'native-denied-write.txt'
  const file = join(agent.workingDir, fileName)
  const initialContent = `KEEP_THE_NATIVE_FILE_${randomUUID()}\n`
  const callId = 'gemini-denied-write'
  const operation = await createNativePermissionFileWrite(context, { fileName, callId, outputPrefix: 'UNAPPROVED_WRITE', initialContent })
  await exerciseNativePermissionDecision(context, {
    toolCall: operation.toolCall,
    decision: 'deny',
    beforeDecision: operation.beforeDecision,
    nativeProof: (request) => {
      expect(readFileSync(file, 'utf8')).toBe(initialContent)
      expect(nativeToolResultContent(request, callId)).toEqual({ error: `Tool "${GEMINI_TOOL.RunShellCommand}" was canceled by the user.` })
    },
  })
  // Gemini renders the call as <tool>__<call ID>. The refused call reads declined before and after a reload.
  const refusal = `Tool "${GEMINI_TOOL.RunShellCommand}" was canceled by the user.`
  await expectDeclinedToolRow(page, `${GEMINI_TOOL.RunShellCommand}__${callId}`, refusal)
  await page.reload()
  await openWorkspace(page, authenticatedGeminiWorkspace.workspaceId)
  await expectDeclinedToolRow(page, `${GEMINI_TOOL.RunShellCommand}__${callId}`, refusal)
})

/**
 * A denied `write_file`. Gemini CLI opens the call with the proposed diff and the file in
 * `locations`. It states no raw input. The failed update replaces the diff with the
 * refusal and states no file. The request row heads the call with the file, because a
 * paired result row draws no header. The result row states the refusal. Neither row draws
 * the proposed text.
 */
geminiTest('reads a denied file write as declined, with its file and no proposed text', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  const fileName = 'native-denied-file-write.txt'
  const file = join(agent.workingDir, fileName)
  const initialContent = `KEEP_THE_NATIVE_FILE_${randomUUID()}\n`
  const proposed = `PROPOSED_NATIVE_TEXT_${randomUUID()}\n`
  writeFileSync(file, initialContent)
  const callId = 'gemini-denied-file-write'
  const refusal = `Tool "${GEMINI_TOOL.WriteFile}" was canceled by the user.`
  await exerciseNativePermissionDecision(context, {
    toolCall: writeToolCall(AgentProvider.GEMINI_CLI, callId, { path: file, content: proposed }),
    decision: 'deny',
    beforeDecision: () => {
      expect(readFileSync(file, 'utf8')).toBe(initialContent)
    },
    nativeProof: (request) => {
      expect(readFileSync(file, 'utf8')).toBe(initialContent)
      expect(nativeToolResultContent(request, callId)).toEqual({ error: refusal })
    },
  })
  // Gemini renders the call as <tool>__<call ID>. The checks hold before and after a reload.
  const renderedCallId = `${GEMINI_TOOL.WriteFile}__${callId}`
  const callRows = messageBubbles(page).and(page.locator(`[data-tool-call-id="${renderedCallId}"]`))
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await openWorkspace(page, authenticatedGeminiWorkspace.workspaceId)
    }
    await expectDeclinedToolRow(page, renderedCallId, refusal)
    await expect(callRows.and(page.locator('[data-tool-row-role="request"]'))).toContainText(fileName)
    await expect(callRows.filter({ hasText: proposed.trim() })).toHaveCount(0)
  }
})

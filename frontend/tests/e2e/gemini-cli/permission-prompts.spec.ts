import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { GEMINI_TOOL } from '../../../src/generated/contracts/gemini-protocol'
import { geminiTest } from '../gemini-fixtures'
import { cssAttributeValue } from '../helpers/cssAttribute'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision, exerciseNativePermissionReason, exerciseNativePermissionWrite, exerciseRememberedAllow, expectDeclinedToolRow, expectDeclinedToolRowAcrossReload } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResultContent } from '../helpers/nativeToolResult'
import { bashToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { messageBubbles, openWorkspace, savedControlAnswer, toolCallRow } from '../helpers/ui'

geminiTest('requires a native permission decision before a real file change', async ({ native }) => {
  await exerciseNativePermissionWrite(native, {
    // The saved row reads the name of Gemini's own `proceed_once` option.
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Allow'),
  })
})

/**
 * Deny selects the ACP option `cancel`.
 * Gemini CLI then fails the tool and sends the refusal to the model as the function response of the same call.
 * The agent loop continues with that response, so the next model request carries it.
 */
geminiTest('sends the exact native refusal after a Deny decision and keeps the file bytes', async ({ native }) => {
  const { page } = native
  const agent = await currentNativeAgent(native)
  const fileName = 'native-denied-write.txt'
  const file = join(agent.workingDir, fileName)
  const initialContent = `KEEP_THE_NATIVE_FILE_${randomUUID()}\n`
  const callId = 'gemini-denied-write'
  const refusal = `Tool "${GEMINI_TOOL.RunShellCommand}" was canceled by the user.`
  const operation = await createNativePermissionFileWrite(native, { fileName, callId, outputPrefix: 'UNAPPROVED_WRITE', initialContent })
  await exerciseNativePermissionDecision(native, {
    toolCall: operation.toolCall,
    decision: 'deny',
    beforeDecision: operation.beforeDecision,
    nativeProof: (request) => {
      expect(readFileSync(file, 'utf8')).toBe(initialContent)
      expect(nativeToolResultContent(request, callId)).toEqual({ error: refusal })
    },
    // Gemini renders the call as <tool>__<call ID>. The refused call reads declined before and after a reload.
    viewProof: async () => {
      // The saved row reads the name of Gemini's own `cancel` option.
      await expect(savedControlAnswer(page)).toHaveText('Reject')
      await expectDeclinedToolRowAcrossReload(native, `${GEMINI_TOOL.RunShellCommand}__${callId}`, refusal)
    },
  })
})

/**
 * A denied `write_file`. Gemini CLI opens the call with the proposed diff and the file in
 * `locations`. It states no raw input. The failed update replaces the diff with the
 * refusal and states no file. The request row heads the call with the file, because a
 * paired result row draws no header. The result row states the refusal. Neither row draws
 * the proposed text.
 */
geminiTest('reads a denied file write as declined, with its file and no proposed text', async ({ native }) => {
  const { page } = native
  const agent = await currentNativeAgent(native)
  const fileName = 'native-denied-file-write.txt'
  const file = join(agent.workingDir, fileName)
  const initialContent = `KEEP_THE_NATIVE_FILE_${randomUUID()}\n`
  const proposed = `PROPOSED_NATIVE_TEXT_${randomUUID()}\n`
  writeFileSync(file, initialContent)
  const callId = 'gemini-denied-file-write'
  const refusal = `Tool "${GEMINI_TOOL.WriteFile}" was canceled by the user.`
  // Gemini renders the call as <tool>__<call ID>. The checks hold before and after a reload.
  const renderedCallId = `${GEMINI_TOOL.WriteFile}__${callId}`
  const callRows = messageBubbles(page).and(page.locator(`[data-tool-call-id="${cssAttributeValue(renderedCallId)}"]`))
  await exerciseNativePermissionDecision(native, {
    toolCall: writeToolCall(native.provider, callId, { path: file, content: proposed }),
    decision: 'deny',
    beforeDecision: () => {
      expect(readFileSync(file, 'utf8')).toBe(initialContent)
    },
    nativeProof: (request) => {
      expect(readFileSync(file, 'utf8')).toBe(initialContent)
      expect(nativeToolResultContent(request, callId)).toEqual({ error: refusal })
    },
    viewProof: async () => {
      for (const reload of [false, true]) {
        if (reload) {
          await page.reload()
          await openWorkspace(page, native.workspaceId)
        }
        await expectDeclinedToolRow(page, renderedCallId, refusal)
        await expect(toolCallRow(page, renderedCallId, 'request')).toContainText(fileName)
        await expect(callRows.filter({ hasText: proposed.trim() })).toHaveCount(0)
      }
    },
  })
})

// The ACP reply selects an option, and an option carries no text. The reason follows as the reader's next message.
geminiTest('sends the reader\'s typed refusal reason as the next message', async ({ native }) => {
  const file = join((await currentNativeAgent(native)).workingDir, 'native-reason-write.txt')
  await exerciseNativePermissionReason(native, {
    toolCall: bashToolCall(native.provider, 'gemini-reason-write', `printf refused > ${quotePosixShellArgument(file)}`),
    route: 'next-message',
    expectNotRun: () => expect(existsSync(file)).toBe(false),
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Reject'),
  })
})

// Gemini's request offers "Allow for this session", and Gemini keeps it in the session, so the same command later
// runs with no request.
geminiTest('a session answer covers the same command in the next turn', async ({ native }) => {
  const file = join((await currentNativeAgent(native)).workingDir, 'native-session-write.txt')
  // Each run appends the marker, so the file states how many runs happened.
  const command = `printf gemini-session >> ${quotePosixShellArgument(file)}`
  await exerciseRememberedAllow(native, {
    scope: 'Session',
    firstCall: bashToolCall(native.provider, 'gemini-session-first', command),
    secondCall: bashToolCall(native.provider, 'gemini-session-second', command),
    beforeDecision: () => expect(existsSync(file)).toBe(false),
    firstProof: () => expect(readFileSync(file, 'utf8')).toBe('gemini-session'),
    secondProof: () => expect(readFileSync(file, 'utf8')).toBe('gemini-sessiongemini-session'),
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Allow for this session'),
  })
})

import type { Page } from '@playwright/test'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest, expect } from './cline-fixtures'
import { contentText, isRecord } from './helpers/mockModelScript'
import { bashToolCall, editToolCall, readToolCall, writeToolCall } from './helpers/providerToolCalls'
import { messageContents, sendMessage, waitForAgentIdle } from './helpers/ui'

/**
 * 241 — Cline tool execution.
 *
 * Each call is Cline's own tool, which the mock scripts and Cline's own executor
 * runs in the agent's working directory. So each row draws the result record that
 * Cline itself reports. The agent runs in Auto-approve, so no banner stops a call;
 * 242 covers the banners.
 */
clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

/** The visible chat text, joined. */
async function chatText(page: Page): Promise<string> {
  return (await messageContents(page).allTextContents()).join(' ')
}

/**
 * The text of the tool message that answers one call in a Chat Completions request.
 * Cline calls the mock through its `openai-compatible` provider, which speaks that
 * protocol.
 */
function toolMessageText(body: unknown, callId: string): string {
  const messages = isRecord(body) && Array.isArray(body.messages) ? body.messages : []
  return messages
    .filter((message): message is Record<string, unknown> => isRecord(message) && message.role === 'tool' && message.tool_call_id === callId)
    .map(message => contentText(message.content))
    .join('\n')
}

clineTest.describe('Cline tool execution', () => {
  clineTest('draws the output of a command', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    // The command text states no `cline-42`, so only the command's own output can put
    // it on the page.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CLINE, 'echo-call', 'echo "cline-$((40 + 2))"')] },
      { text: 'The command printed its number.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect.poll(() => chatText(page)).toContain('cline-42')
    // Cline states the result as a list of records; the row draws the output, not the
    // record.
    expect(await chatText(page)).not.toContain('"success"')
    // The executor ran the call: its record reached the next model call.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(followUp?.body)).toContain('cline-42')
  })

  clineTest('draws the lines a read returns', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    const notes = join(authenticatedClineWorkspace.workingDir, 'notes.txt')
    writeFileSync(notes, 'cline-read-1\ncline-read-2\ncline-read-3\n')
    await modelScript.queue(
      { toolCalls: [readToolCall(AgentProvider.CLINE, 'read-notes', notes)] },
      { text: 'I read the notes.' },
    )
    await sendMessage(page, modelScript.prompt('Read the notes back.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect.poll(() => chatText(page)).toContain('cline-read-3')
    // Cline numbers each line `<n> | `. The row draws the file's own lines.
    expect(await chatText(page)).not.toContain('3 | cline-read-3')
  })

  clineTest('draws the diff of an edit', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    const path = join(authenticatedClineWorkspace.workingDir, 'parity.ts')
    writeFileSync(path, 'const parityBefore = 1\n')
    await modelScript.queue(
      { toolCalls: [editToolCall(AgentProvider.CLINE, 'parity-edit', { path, before: 'const parityBefore = 1', after: 'const parityAfter = 2' })] },
      { text: 'I changed parity.ts.' },
    )
    await sendMessage(page, modelScript.prompt('Change parity.ts.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    expect(readFileSync(path, 'utf8')).toBe('const parityAfter = 2\n')
    const diff = page.locator('[data-file-diff]:visible')
    await expect(diff.filter({ hasText: 'const parityAfter = 2' }).first()).toBeVisible()
    await expect(diff.filter({ hasText: 'const parityBefore = 1' }).first()).toBeVisible()
  })

  clineTest('creates the file that a write call states', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    const path = join(authenticatedClineWorkspace.workingDir, 'note.txt')
    expect(existsSync(path)).toBe(false)
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.CLINE, 'write-call', { path, content: 'cline was here' })] },
      { text: 'I wrote the note.' },
    )
    await sendMessage(page, modelScript.prompt('Write the note.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    expect(readFileSync(path, 'utf8')).toContain('cline was here')
    await expect.poll(() => chatText(page)).toContain('note.txt')
  })

  clineTest('draws the error of a failed command, and the model reads why', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    // The command text states no `cline-fail-77`, so only the command's own stderr can
    // put it on the page or in the next model call. Both also carry the command text,
    // so a marker that the command text spells proves nothing.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CLINE, 'fail-call', 'echo "cline-fail-$((70 + 7))" >&2; exit 3')] },
      { text: 'The command failed.' },
    )
    await sendMessage(page, modelScript.prompt('Run the failing command.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    // A collapsed row shows the stderr text, so the reader sees why with no expansion.
    const tools = page.locator('[data-tool-message]:visible')
    await expect(tools.filter({ hasText: 'cline-fail-77' }).first()).toBeVisible()
    // Cline states the exit code in words only, and the command header reads it.
    await expect(tools.filter({ hasText: 'Error (exit 3)' }).first()).toBeVisible()
    // The header states the code. The body does not state it again.
    await expect(tools.filter({ hasText: 'Command exited with code' })).toHaveCount(0)
    // The model reads why: the result of this call states the stderr text and the code.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    const answer = toolMessageText(followUp?.body, 'fail-call')
    expect(answer).toContain('cline-fail-77')
    expect(answer).toContain('Command exited with code 3')
  })
})

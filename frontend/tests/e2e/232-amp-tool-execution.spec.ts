import type { Page } from '@playwright/test'
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest, expect } from './amp-fixtures'
import { bashToolCall, editToolCall, readToolCall, writeToolCall } from './helpers/providerToolCalls'
import { messageContents, sendMessage, waitForAgentIdle } from './helpers/ui'

/**
 * 232 — Amp tool execution.
 *
 * Each call is Amp's own tool, scripted at the mock's Amp surface and leased to the
 * real CLI's executor, which runs it in the agent's working directory. So each row
 * draws the result record that Amp itself reports. The agent runs in Allow All, so no
 * banner stops a call; 233 covers the banners.
 */
ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

/** The visible chat text, joined. */
async function chatText(page: Page): Promise<string> {
  return (await messageContents(page).allTextContents()).join(' ')
}

ampTest.describe('Amp tool execution', () => {
  ampTest('draws the output of a command', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    void authenticatedAmpWorkspace
    // The command text states no `amp-42`, so only the command's own output can put
    // it on the page.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.AMP, 'echo-call', 'echo "amp-$((40 + 2))"')] },
      { text: 'The command printed its number.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect.poll(() => chatText(page)).toContain('amp-42')
    // Amp states the result as a JSON record; the row draws its output, not the record.
    expect(await chatText(page)).not.toContain('"exitCode"')
    // The executor ran the call: its record reached the next inference.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(followUp?.body)).toContain('amp-42')
  })

  ampTest('draws the lines a read returns', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    const notes = join(authenticatedAmpWorkspace.workingDir, 'notes.txt')
    writeFileSync(notes, 'amp-read-1\namp-read-2\namp-read-3\n')
    await modelScript.queue(
      { toolCalls: [readToolCall(AgentProvider.AMP, 'read-notes', notes)] },
      { text: 'I read the notes.' },
    )
    await sendMessage(page, modelScript.prompt('Read the notes back.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect.poll(() => chatText(page)).toContain('amp-read-3')
    // Amp numbers each line `<n>: `. The row draws the file's own lines.
    expect(await chatText(page)).not.toContain('3: amp-read-3')
  })

  ampTest('draws the diff of an edit', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    const path = join(authenticatedAmpWorkspace.workingDir, 'parity.ts')
    writeFileSync(path, 'const parityBefore = 1\n')
    await modelScript.queue(
      { toolCalls: [editToolCall(AgentProvider.AMP, 'parity-edit', { path, before: 'const parityBefore = 1', after: 'const parityAfter = 2' })] },
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

  ampTest('writes the file that a write call states', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    const path = join(authenticatedAmpWorkspace.workingDir, 'note.txt')
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.AMP, 'write-call', { path, content: 'amp was here' })] },
      { text: 'I wrote the note.' },
    )
    await sendMessage(page, modelScript.prompt('Write the note.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    expect(readFileSync(path, 'utf8')).toBe('amp was here\n')
    await expect.poll(() => chatText(page)).toContain('note.txt')
  })
})

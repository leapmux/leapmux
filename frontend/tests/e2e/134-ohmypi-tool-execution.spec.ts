import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall, editToolCall, readToolCall, writeToolCall } from './helpers/providerToolCalls'
import { createTestDirectory } from './helpers/runDirectory'
import { messageContents, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from './ohmypi-fixtures'

/**
 * 134 — Oh My Pi tool execution.
 *
 * Each call is omp's own tool, scripted at the mock endpoint and run by the real
 * CLI, so each row draws the result shape that omp reports. The agent runs in
 * omp's `yolo` mode, so no approval stops a call; 135 covers the approvals.
 */
ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

/** The visible chat text, joined. */
async function chatText(page: Parameters<typeof messageContents>[0]): Promise<string> {
  return (await messageContents(page).allTextContents()).join(' ')
}

ohMyPiTest.describe('Oh My Pi tool execution', () => {
  ohMyPiTest('draws the output of a command', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    // The command text states no `omp-42`, so only the command's own output
    // can put it on the page.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.OH_MY_PI, 'echo-call', 'echo "omp-$((40 + 2))"')] },
      { text: 'The command printed its number.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect.poll(() => chatText(page)).toContain('omp-42')
    // omp appends a `Wall time: <n> seconds` notice to every output. The
    // extractor drops it, because the row already states how the call ended.
    expect(await chatText(page)).not.toContain('Wall time:')
  })

  ohMyPiTest('draws the lines a read returns', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    // `seq` writes the numbers, so no command text holds `omp-read-3`: only the
    // read's own result can put it on the page.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.OH_MY_PI, 'seed-notes', 'seq 3 | sed "s/^/omp-read-/" > notes.txt')] },
      { toolCalls: [readToolCall(AgentProvider.OH_MY_PI, 'read-notes', 'notes.txt')] },
      { text: 'I read the notes.' },
    )
    await sendMessage(page, modelScript.prompt('Create the notes and read them back.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect.poll(() => chatText(page)).toContain('omp-read-3')
    // The E2E profile's `replace` edit makes omp print the bare file text, with no
    // header and no line numbers. The numbers come from `details.displayContent`
    // alone, and a card that fell back to the raw text draws the same words with no
    // numbered row. So the numbered row proves that the extractor read the details.
    await expect(messageContents(page).locator('[data-line-num="3"]').filter({ hasText: 'omp-read-3' })).toHaveCount(1)
  })

  ohMyPiTest('draws the diff of an edit', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    // The file must exist before the edit, so the edit states both sides.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.OH_MY_PI, 'seed-parity', 'printf "const parityBefore = 1\\n" > parity.ts')] },
      { toolCalls: [readToolCall(AgentProvider.OH_MY_PI, 'parity-read', 'parity.ts')] },
      { toolCalls: [editToolCall(AgentProvider.OH_MY_PI, 'parity-edit', { path: 'parity.ts', before: 'const parityBefore = 1', after: 'const parityAfter = 2' })] },
      { text: 'I changed parity.ts.' },
    )
    await sendMessage(page, modelScript.prompt('Change parity.ts.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    const diff = page.locator('[data-file-diff]:visible')
    await expect(diff.filter({ hasText: 'const parityAfter = 2' }).first()).toBeVisible()
    await expect(diff.filter({ hasText: 'const parityBefore = 1' }).first()).toBeVisible()
  })

  ohMyPiTest('writes the file that a write call states', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    const path = join(createTestDirectory('omp-write-'), 'note.txt')
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.OH_MY_PI, 'write-call', { path, content: 'omp was here\n' })] },
      { text: 'I wrote the note.' },
    )
    await sendMessage(page, modelScript.prompt('Write the note.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    expect(readFileSync(path, 'utf8')).toBe('omp was here\n')
    await expect.poll(() => chatText(page)).toContain('note.txt')
  })
})

import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { exerciseFileEditSequence, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall, readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { chatText, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'

import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The installed agent runs its native file tools on real files:
 *
 * - Write.
 * - Edit.
 * - Read.
 *
 * The transcript must show the native read and the edit diff.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest.describe('Oh My Pi tool execution', () => {
  ohMyPiTest('draws the lines a read returns', async ({ native }) => {
    const { page, modelScript } = native
    const agent = await currentNativeAgent(native)
    if (!agent.workingDir)
      throw new Error('The active native agent has no working directory.')
    const notes = join(createNativeToolDirectory(agent.workingDir), 'notes.txt')
    // `seq` writes the numbers, so no command text holds `omp-read-3`: only the
    // read's own result can put it on the page.
    const start = await modelScript.queue(
      { toolCalls: [bashToolCall(native.provider, 'seed-notes', `seq 3 | sed "s/^/omp-read-/" > ${quotePosixShellArgument(notes)}`)] },
      { toolCalls: [readToolCall(native.provider, 'read-notes', notes)] },
      nativeTextStep(native, 'I read the notes.'),
    )
    await sendMessage(page, modelScript.prompt('Create the notes and read them back.'))
    await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)

    await expect.poll(() => chatText(page)).toContain('omp-read-3')
    // The E2E profile's `replace` edit makes omp print the bare file text, with no
    // header and no line numbers. The numbers come from `details.displayContent`
    // alone, and a card that fell back to the raw text draws the same words with no
    // numbered row. So the numbered row proves that the extractor read the details.
    await expect(messageContents(page).locator('[data-line-num="3"]').filter({ hasText: 'omp-read-3' })).toHaveCount(1)
  })

  ohMyPiTest('draws the diff of an edit', async ({ native }) => {
    const agent = await currentNativeAgent(native)
    if (!agent.workingDir)
      throw new Error('The active native agent has no working directory.')
    // The sequence seeds the file before the edit, so the edit states both sides.
    await exerciseFileEditSequence(native, { workingDir: createNativeToolDirectory(agent.workingDir), fileName: 'parity.ts' })
  })

  ohMyPiTest('writes the file that a write call states', async ({ native }) => {
    const agent = await currentNativeAgent(native)
    if (!agent.workingDir)
      throw new Error('The active native agent has no working directory.')
    const path = join(createNativeToolDirectory(agent.workingDir), 'note.txt')
    await runNativeToolTurn(native, {
      toolCalls: [writeToolCall(native.provider, 'write-call', { path, content: 'omp was here\n' })],
      prompt: 'Write the note.',
      answer: 'I wrote the note.',
    })

    expect(readFileSync(path, 'utf8')).toBe('omp was here\n')
    await expect.poll(() => chatText(native.page)).toContain('note.txt')
  })
})

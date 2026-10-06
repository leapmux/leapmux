import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { bashToolCall, editToolCall, readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { chatText, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'

import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The installed agent writes, edits, and reads real files. The transcript must show the native read and edit diff.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest.describe('Oh My Pi tool execution', () => {
  ohMyPiTest('draws the lines a read returns', async ({ authenticatedOhMyPiWorkspace, page, modelScript, leapmuxServer }) => {
    void authenticatedOhMyPiWorkspace
    const agent = await currentNativeAgent({ page, leapmuxServer })
    if (!agent.workingDir)
      throw new Error('The active native agent has no working directory.')
    const notes = join(createNativeToolDirectory(agent.workingDir), 'notes.txt')
    // `seq` writes the numbers, so no command text holds `omp-read-3`: only the
    // read's own result can put it on the page.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.OH_MY_PI, 'seed-notes', `seq 3 | sed "s/^/omp-read-/" > ${quotePosixShellArgument(notes)}`)] },
      { toolCalls: [readToolCall(AgentProvider.OH_MY_PI, 'read-notes', notes)] },
      { text: 'I read the notes.' },
    )
    await sendMessage(page, modelScript.prompt('Create the notes and read them back.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect.poll(() => chatText(page)).toContain('omp-read-3')
    // The E2E profile's `replace` edit makes omp print the bare file text, with no
    // header and no line numbers. The numbers come from `details.displayContent`
    // alone, and a card that fell back to the raw text draws the same words with no
    // numbered row. So the numbered row proves that the extractor read the details.
    await expect(messageContents(page).locator('[data-line-num="3"]').filter({ hasText: 'omp-read-3' })).toHaveCount(1)
  })

  ohMyPiTest('draws the diff of an edit', async ({ authenticatedOhMyPiWorkspace, page, modelScript, leapmuxServer }) => {
    void authenticatedOhMyPiWorkspace
    const agent = await currentNativeAgent({ page, leapmuxServer })
    if (!agent.workingDir)
      throw new Error('The active native agent has no working directory.')
    const path = join(createNativeToolDirectory(agent.workingDir), 'parity.ts')
    // The file must exist before the edit, so the edit states both sides.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.OH_MY_PI, 'seed-parity', `printf "const parityBefore = 1\\n" > ${quotePosixShellArgument(path)}`)] },
      { toolCalls: [readToolCall(AgentProvider.OH_MY_PI, 'parity-read', path)] },
      { toolCalls: [editToolCall(AgentProvider.OH_MY_PI, 'parity-edit', { path, before: 'const parityBefore = 1', after: 'const parityAfter = 2' })] },
      { text: 'I changed parity.ts.' },
    )
    await sendMessage(page, modelScript.prompt('Change parity.ts.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const diff = page.locator('[data-file-diff]:visible')
    await expect(diff.filter({ hasText: 'const parityAfter = 2' }).first()).toBeVisible()
    await expect(diff.filter({ hasText: 'const parityBefore = 1' }).first()).toBeVisible()
  })

  ohMyPiTest('writes the file that a write call states', async ({ authenticatedOhMyPiWorkspace, page, modelScript, leapmuxServer }) => {
    void authenticatedOhMyPiWorkspace
    const agent = await currentNativeAgent({ page, leapmuxServer })
    if (!agent.workingDir)
      throw new Error('The active native agent has no working directory.')
    const path = join(createNativeToolDirectory(agent.workingDir), 'note.txt')
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.OH_MY_PI, 'write-call', { path, content: 'omp was here\n' })] },
      { text: 'I wrote the note.' },
    )
    await sendMessage(page, modelScript.prompt('Write the note.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    expect(readFileSync(path, 'utf8')).toBe('omp was here\n')
    await expect.poll(() => chatText(page)).toContain('note.txt')
  })
})

import { join } from 'node:path'
import { expect } from '@playwright/test'

import { codewhaleTest } from '../codewhale-fixtures'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { editToolCall, readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, fileChangeRow, sendMessage, transcriptRows, waitForAgentIdle } from '../helpers/ui'
import { runWithoutApprovals } from './toolScenarios'

codewhaleTest.describe('Codewhale tool execution', () => {
  codewhaleTest('writes, edits and reads a file, and draws the edit as a diff', async ({ native }) => {
    const { page, modelScript } = native
    const agent = await currentNativeAgent(native)
    if (!agent.workingDir)
      throw new Error('The active native agent has no working directory.')
    const path = join(createNativeToolDirectory(agent.workingDir), 'notes.txt')
    await runWithoutApprovals(page)
    // The native calls use one literal private path and keep the original basename.
    const start = await modelScript.queue(
      { toolCalls: [writeToolCall(native.provider, 'write-call', { path, content: 'alpha\nold line\n' })] },
      { toolCalls: [editToolCall(native.provider, 'edit-call', { path, before: 'old line', after: 'new line' })] },
      { toolCalls: [readToolCall(native.provider, 'read-call', path)] },
      nativeTextStep(native, 'notes.txt now holds the new line.'),
    )
    await sendMessage(page, modelScript.prompt('Create notes.txt, change its old line, and read it back.'))
    await modelScript.waitForSteps(start + 4)
    await waitForAgentIdle(page)

    // The edit's header counts the runtime's own diff: one line out, one line in.
    const edit = fileChangeRow(page, 'notes.txt')
    await expect(edit).toBeVisible()
    await expect(edit.getByTestId('git-diff-stats')).toContainText('+1')
    await expect(edit.getByTestId('git-diff-stats')).toContainText('-1')
    // The diff itself is the one row that holds both sides of the change. The
    // prompt and the write hold the old line alone, and the read and the reply
    // hold the new line alone.
    await expect(transcriptRows(page).filter({ hasText: 'old line' }).filter({ hasText: 'new line' }).first()).toBeVisible()

    // The read draws the file as the edit left it.
    await expect(transcriptRows(page).filter({ hasText: 'alpha' }).filter({ hasText: 'new line' }).filter({ hasNotText: 'old line' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'notes.txt now holds the new line.' })).toBeVisible()
  })
})

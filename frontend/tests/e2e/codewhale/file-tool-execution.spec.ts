import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'

import { codewhaleTest } from '../codewhale-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { runNativeToolSteps } from '../helpers/nativeToolExecution'
import { codewhaleEditToolCall, editToolCall, readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, expectNoControlBanner, fileChangeRow, sendMessage, transcriptRows } from '../helpers/ui'
import { runWithoutApprovals } from './toolScenarios'

codewhaleTest.describe('Codewhale tool execution', () => {
  codewhaleTest('writes, edits and reads a file, and draws the edit as a diff', async ({ native }) => {
    const { page } = native
    const agent = await currentNativeAgent(native)
    if (!agent.workingDir)
      throw new Error('The active native agent has no working directory.')
    const path = join(createNativeToolDirectory(agent.workingDir), 'notes.txt')
    await runWithoutApprovals(page)
    // The native calls use one literal private path and keep the original basename.
    await runNativeToolSteps(native, {
      steps: [
        {
          toolCalls: [
            writeToolCall(native.provider, 'write-call', { path, content: 'alpha\nold line\n' }),
          ],
        },
        {
          toolCalls: [
            editToolCall(native.provider, 'edit-call', { path, before: 'old line', after: 'new line' }),
          ],
        },
        {
          toolCalls: [
            readToolCall(native.provider, 'read-call', path),
          ],
        },
      ],
      prompt: 'Create notes.txt, change its old line, and read it back.',
      answer: 'notes.txt now holds the new line.',
      permissions: 'none',
    })

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

for (const encoded of [false, true]) {
  codewhaleTest(`renders the current ${encoded ? 'encoded' : 'array'} edit list with its additional top-level substitution`, async ({ native }) => {
    const { page, modelScript } = native
    const agent = await currentNativeAgent(native)
    const path = join(createNativeToolDirectory(agent.workingDir), 'current-variants.txt')
    writeFileSync(path, 'first-original\nsecond-original\n')
    const edits = [
      { oldText: 'first-original', newText: 'FIRST-REPLACED' },
    ]
    await modelScript.queue(
      {
        toolCalls: [
          codewhaleEditToolCall('current-edit-variants', {
            path,
            edits: encoded
              ? JSON.stringify(edits)
              : edits,
            oldText: 'second-original',
            newText: 'SECOND-REPLACED',
          }),
        ],
      },
      { text: 'Both requested substitutions completed.' },
    )
    await sendMessage(page, modelScript.prompt('Apply the two requested substitutions to the prepared file.'))
    await modelScript.waitForSteps()
    await expectNoControlBanner(page)
    const diffs = page.locator('[data-file-diff]:visible')
    await expect(diffs.filter({ hasText: 'FIRST-REPLACED' }).first()).toBeVisible()
    await expect(diffs.filter({ hasText: 'SECOND-REPLACED' }).first()).toBeVisible()
    expect(readFileSync(path, 'utf8')).toBe('FIRST-REPLACED\nSECOND-REPLACED\n')
    await expect(assistantBubbles(page).filter({ hasText: 'Both requested substitutions completed.' })).toBeVisible()
  })
}

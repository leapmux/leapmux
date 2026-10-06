import { existsSync, readFileSync, writeFileSync } from 'node:fs'

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { clineTest } from '../cline-fixtures'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { expectFileDiff, PARITY_AFTER, PARITY_BEFORE, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { editToolCall, readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { chatText } from '../helpers/ui'

/**
 * The installed agent runs its native file tools on real files:
 *
 * - Write.
 * - Edit.
 * - Read.
 *
 * The transcript must show the native read and the edit diff.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 */
clineTest.describe('Cline tool execution', () => {
  clineTest('draws the lines a read returns', async ({ authenticatedClineWorkspace, native }) => {
    const notes = join(createNativeToolDirectory(authenticatedClineWorkspace.workingDir), 'notes.txt')
    writeFileSync(notes, 'cline-read-1\ncline-read-2\ncline-read-3\n')
    await runNativeToolTurn(native, {
      toolCalls: [readToolCall(native.provider, 'read-notes', notes)],
      prompt: 'Read the notes back.',
      answer: 'I read the notes.',
    })

    await expect.poll(() => chatText(native.page)).toContain('cline-read-3')
    // Cline numbers each line `<n> | `. The row draws the file's own lines.
    expect(await chatText(native.page)).not.toContain('3 | cline-read-3')
  })

  clineTest('draws the diff of an edit', async ({ authenticatedClineWorkspace, native }) => {
    const path = join(createNativeToolDirectory(authenticatedClineWorkspace.workingDir), 'parity.ts')
    writeFileSync(path, `${PARITY_BEFORE}\n`)
    await runNativeToolTurn(native, {
      toolCalls: [editToolCall(native.provider, 'parity-edit', { path, before: PARITY_BEFORE, after: PARITY_AFTER })],
      prompt: 'Change parity.ts.',
      answer: 'I changed parity.ts.',
    })

    expect(readFileSync(path, 'utf8')).toBe(`${PARITY_AFTER}\n`)
    await expectFileDiff(native.page, { before: PARITY_BEFORE, after: PARITY_AFTER })
  })

  clineTest('creates the file that a write call states', async ({ authenticatedClineWorkspace, native }) => {
    const path = join(createNativeToolDirectory(authenticatedClineWorkspace.workingDir), 'note.txt')
    expect(existsSync(path)).toBe(false)
    await runNativeToolTurn(native, {
      toolCalls: [writeToolCall(native.provider, 'write-call', { path, content: 'cline was here' })],
      prompt: 'Write the note.',
      answer: 'I wrote the note.',
    })

    expect(readFileSync(path, 'utf8')).toContain('cline was here')
    await expect.poll(() => chatText(native.page)).toContain('note.txt')
  })
})

import { readFileSync, writeFileSync } from 'node:fs'

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { ampTest } from '../amp-fixtures'
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
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 */
ampTest.describe('Amp tool execution', () => {
  ampTest('draws the lines a read returns', async ({ authenticatedAmpWorkspace, native }) => {
    const notes = join(createNativeToolDirectory(authenticatedAmpWorkspace.workingDir), 'notes.txt')
    writeFileSync(notes, 'amp-read-1\namp-read-2\namp-read-3\n')
    await runNativeToolTurn(native, {
      toolCalls: [readToolCall(native.provider, 'read-notes', notes)],
      prompt: 'Read the notes back.',
      answer: 'I read the notes.',
    })

    await expect.poll(() => chatText(native.page)).toContain('amp-read-3')
    // Amp numbers each line `<n>: `. The row draws the file's own lines.
    expect(await chatText(native.page)).not.toContain('3: amp-read-3')
  })

  ampTest('draws the diff of an edit', async ({ authenticatedAmpWorkspace, native }) => {
    const path = join(createNativeToolDirectory(authenticatedAmpWorkspace.workingDir), 'parity.ts')
    writeFileSync(path, `${PARITY_BEFORE}\n`)
    await runNativeToolTurn(native, {
      toolCalls: [editToolCall(native.provider, 'parity-edit', { path, before: PARITY_BEFORE, after: PARITY_AFTER })],
      prompt: 'Change parity.ts.',
      answer: 'I changed parity.ts.',
    })

    expect(readFileSync(path, 'utf8')).toBe(`${PARITY_AFTER}\n`)
    await expectFileDiff(native.page, { before: PARITY_BEFORE, after: PARITY_AFTER })
  })

  ampTest('writes the file that a write call states', async ({ authenticatedAmpWorkspace, native }) => {
    const path = join(createNativeToolDirectory(authenticatedAmpWorkspace.workingDir), 'note.txt')
    await runNativeToolTurn(native, {
      toolCalls: [writeToolCall(native.provider, 'write-call', { path, content: 'amp was here' })],
      prompt: 'Write the note.',
      answer: 'I wrote the note.',
    })

    expect(readFileSync(path, 'utf8')).toBe('amp was here\n')
    await expect.poll(() => chatText(native.page)).toContain('note.txt')
  })
})

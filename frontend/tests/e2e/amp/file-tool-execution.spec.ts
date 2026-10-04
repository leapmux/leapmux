import { readFileSync, writeFileSync } from 'node:fs'

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { editToolCall, readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { chatText, sendMessage, waitForAgentIdle } from '../helpers/ui'

/**
 * The installed agent writes, edits, and reads real files. The transcript must show the native read and edit diff.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 */
ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

ampTest.describe('Amp tool execution', () => {
  ampTest('draws the lines a read returns', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    const notes = join(createNativeToolDirectory(authenticatedAmpWorkspace.workingDir), 'notes.txt')
    writeFileSync(notes, 'amp-read-1\namp-read-2\namp-read-3\n')
    await modelScript.queue(
      { toolCalls: [readToolCall(AgentProvider.AMP, 'read-notes', notes)] },
      { text: 'I read the notes.' },
    )
    await sendMessage(page, modelScript.prompt('Read the notes back.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect.poll(() => chatText(page)).toContain('amp-read-3')
    // Amp numbers each line `<n>: `. The row draws the file's own lines.
    expect(await chatText(page)).not.toContain('3: amp-read-3')
  })

  ampTest('draws the diff of an edit', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    const path = join(createNativeToolDirectory(authenticatedAmpWorkspace.workingDir), 'parity.ts')
    writeFileSync(path, 'const parityBefore = 1\n')
    await modelScript.queue(
      { toolCalls: [editToolCall(AgentProvider.AMP, 'parity-edit', { path, before: 'const parityBefore = 1', after: 'const parityAfter = 2' })] },
      { text: 'I changed parity.ts.' },
    )
    await sendMessage(page, modelScript.prompt('Change parity.ts.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    expect(readFileSync(path, 'utf8')).toBe('const parityAfter = 2\n')
    const diff = page.locator('[data-file-diff]:visible')
    await expect(diff.filter({ hasText: 'const parityAfter = 2' }).first()).toBeVisible()
    await expect(diff.filter({ hasText: 'const parityBefore = 1' }).first()).toBeVisible()
  })

  ampTest('writes the file that a write call states', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    const path = join(createNativeToolDirectory(authenticatedAmpWorkspace.workingDir), 'note.txt')
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.AMP, 'write-call', { path, content: 'amp was here' })] },
      { text: 'I wrote the note.' },
    )
    await sendMessage(page, modelScript.prompt('Write the note.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    expect(readFileSync(path, 'utf8')).toBe('amp was here\n')
    await expect.poll(() => chatText(page)).toContain('note.txt')
  })
})

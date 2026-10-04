import { existsSync, readFileSync, writeFileSync } from 'node:fs'

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { editToolCall, readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { chatText, sendMessage, waitForAgentIdle } from '../helpers/ui'

/**
 * The installed agent writes, edits, and reads real files. The transcript must show the native read and edit diff.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 */
clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

clineTest.describe('Cline tool execution', () => {
  clineTest('draws the lines a read returns', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    const notes = join(createNativeToolDirectory(authenticatedClineWorkspace.workingDir), 'notes.txt')
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
    const path = join(createNativeToolDirectory(authenticatedClineWorkspace.workingDir), 'parity.ts')
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
    const path = join(createNativeToolDirectory(authenticatedClineWorkspace.workingDir), 'note.txt')
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
})

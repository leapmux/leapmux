import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseFileToolExecution, runNativeToolSteps } from '../helpers/nativeToolExecution'
import { readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { chatText } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code tool execution', () => {
  lettaTest('draws the lines a write and a read return', async ({ authenticatedLettaWorkspace, native }) => {
    const notes = join(authenticatedLettaWorkspace.workingDir, 'notes.txt')
    await runNativeToolSteps(native, {
      steps: [
        { toolCalls: [writeToolCall(native.provider, 'write-notes', { path: notes, content: 'letta-write-1\n' })] },
        { toolCalls: [readToolCall(native.provider, 'read-notes', notes)] },
      ],
      prompt: 'Write the notes and read them back.',
      answer: 'I wrote and read the notes.',
      permissions: 'none',
    })

    await expect.poll(() => chatText(native.page)).toContain('letta-write-1')
    // The native tool writes the file in the agent's working directory.
    expect(readFileSync(notes, 'utf8')).toContain('letta-write-1')
  })
})

lettaTest('reads and changes native files and keeps the applied diff after reload', async ({ native }) => {
  await exerciseFileToolExecution(native)
})

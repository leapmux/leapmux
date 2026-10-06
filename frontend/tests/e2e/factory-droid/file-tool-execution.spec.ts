import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { DROID_TOOL } from '../../../src/generated/contracts/droid-protocol'
import { droidTest } from '../droid-fixtures'
import { exerciseFileToolExecution, runNativeToolSteps } from '../helpers/nativeToolExecution'
import { readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { chatText } from '../helpers/ui'
import { readDroidToolResult } from './toolResult'

droidTest.describe('Factory Droid tool execution', () => {
  droidTest('draws the lines a write and a read return', async ({ authenticatedDroidWorkspace, native }) => {
    const notes = join(authenticatedDroidWorkspace.workingDir, 'notes.txt')
    await runNativeToolSteps(native, {
      steps: [
        { toolCalls: [writeToolCall(native.provider, 'write-notes', { path: notes, content: 'droid-write-1\n' })] },
        { toolCalls: [readToolCall(native.provider, 'read-notes', notes)] },
      ],
      prompt: 'Write the notes and read them back.',
      answer: 'I wrote and read the notes.',
      permissions: 'none',
    })

    await expect.poll(() => chatText(native.page)).toContain('droid-write-1')
    // The native tool writes the file in the agent's working directory.
    expect(readFileSync(notes, 'utf8')).toContain('droid-write-1')
  })
})

droidTest('reads and changes native files and keeps the applied diff after reload', async ({ native }) => {
  await exerciseFileToolExecution({ ...native, readToolResult: (request, callId) => readDroidToolResult(request, callId, DROID_TOOL.Read) })
})

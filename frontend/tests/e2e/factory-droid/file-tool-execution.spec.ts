import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { DROID_TOOL } from '../../../src/generated/contracts/droid-protocol'
import { droidTest, expect } from '../droid-fixtures'
import { nativeTextStep } from '../helpers/nativeScenario'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { chatText, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { readDroidToolResult } from './toolResult'

droidTest.describe('Factory Droid tool execution', () => {
  droidTest('draws the lines a write and a read return', async ({ authenticatedDroidWorkspace, native }) => {
    const { page, modelScript } = native
    const notes = join(authenticatedDroidWorkspace.workingDir, 'notes.txt')
    const start = await modelScript.queue(
      { toolCalls: [writeToolCall(native.provider, 'write-notes', { path: notes, content: 'droid-write-1\n' })] },
      { toolCalls: [readToolCall(native.provider, 'read-notes', notes)] },
      nativeTextStep(native, 'I wrote and read the notes.'),
    )
    await sendMessage(page, modelScript.prompt('Write the notes and read them back.'))
    await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)

    await expect.poll(() => chatText(page)).toContain('droid-write-1')
    // The native tool writes the file in the agent's working directory.
    expect(readFileSync(notes, 'utf8')).toContain('droid-write-1')
  })
})

droidTest('reads and changes native files and keeps the applied diff after reload', async ({ native }) => {
  await exerciseFileToolExecution({ ...native, readToolResult: (request, callId) => readDroidToolResult(request, callId, DROID_TOOL.Read) })
})

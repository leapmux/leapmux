import { join } from 'node:path'
import { DROID_TOOL } from '../../../src/generated/contracts/droid-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { DROID_TITLE_RULE, droidTest, expect } from '../droid-fixtures'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'
import { readDroidToolResult } from './toolResult'

droidTest.describe('Factory Droid tool execution', () => {
  const PROVIDER = AgentProvider.DROID

  async function chatText(page: Parameters<typeof messageContents>[0]): Promise<string> {
    return (await messageContents(page).allTextContents()).join(' ')
  }

  droidTest('draws the lines a write and a read return', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    const notes = join(authenticatedDroidWorkspace.workingDir, 'notes.txt')
    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue(
      { toolCalls: [writeToolCall(PROVIDER, 'write-notes', { path: notes, content: 'droid-write-1\n' })] },
      { toolCalls: [readToolCall(PROVIDER, 'read-notes', notes)] },
      { text: 'I wrote and read the notes.' },
    )
    await sendMessage(page, modelScript.prompt('Write the notes and read them back.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect.poll(() => chatText(page)).toContain('droid-write-1')
    // The native tool writes the file in the agent's working directory.
    expect(await import('node:fs').then(fs => fs.readFileSync(notes, 'utf8'))).toContain('droid-write-1')
  })
})

droidTest('reads and changes native files and keeps the applied diff after reload', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await exerciseFileToolExecution({ ...context, readToolResult: (request, callId) => readDroidToolResult(request, callId, DROID_TOOL.Read) })
})

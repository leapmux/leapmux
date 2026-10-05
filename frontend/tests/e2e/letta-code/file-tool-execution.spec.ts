import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest.describe('Letta Code tool execution', () => {
  lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

  const PROVIDER = AgentProvider.LETTA

  async function chatText(page: Parameters<typeof messageContents>[0]): Promise<string> {
    return (await messageContents(page).allTextContents()).join(' ')
  }

  lettaTest('draws the lines a write and a read return', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    const notes = join(authenticatedLettaWorkspace.workingDir, 'notes.txt')
    await modelScript.rule(LETTA_TITLE_RULE)
    await modelScript.queue(
      { toolCalls: [writeToolCall(PROVIDER, 'write-notes', { path: notes, content: 'letta-write-1\n' })] },
      { toolCalls: [readToolCall(PROVIDER, 'read-notes', notes)] },
      { text: 'I wrote and read the notes.' },
    )
    await sendMessage(page, modelScript.prompt('Write the notes and read them back.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect.poll(() => chatText(page)).toContain('letta-write-1')
    // The native tool writes the file in the agent's working directory.
    expect(readFileSync(notes, 'utf8')).toContain('letta-write-1')
  })
})

lettaTest('reads and changes native files and keeps the applied diff after reload', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseFileToolExecution(context)
})

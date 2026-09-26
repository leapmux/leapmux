import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall, readToolCall, writeToolCall } from './helpers/providerToolCalls'
import { messageContents, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from './letta-fixtures'

/**
 * 263 — Letta Code tool execution.
 *
 * Each call is Letta's own tool, which the mock scripts and Letta's own executor
 * runs in the agent's working directory. So each row draws the result that Letta
 * itself reports. The agent runs in Unrestricted, so no banner stops a call; 264
 * covers the banners.
 */
lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.LETTA

/** The visible chat text, joined. */
async function chatText(page: Parameters<typeof messageContents>[0]): Promise<string> {
  return (await messageContents(page).allTextContents()).join(' ')
}

lettaTest.describe('Letta Code tool execution', () => {
  lettaTest('draws the output of a command', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    // The command text states no `letta-42`, so only the command's own output can
    // put it on the page.
    await modelScript.rule(LETTA_TITLE_RULE)
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'echo-call', 'echo "letta-$((40 + 2))"')] },
      { text: 'The command printed its number.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect.poll(() => chatText(page)).toContain('letta-42')
    // The executor ran the call: its result reached the next model call.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(followUp?.body)).toContain('letta-42')
  })

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
    await waitForAgentIdle(page, 180_000)

    await expect.poll(() => chatText(page)).toContain('letta-write-1')
    // The file really landed in the agent's working directory.
    expect(readFileSync(notes, 'utf8')).toContain('letta-write-1')
  })
})

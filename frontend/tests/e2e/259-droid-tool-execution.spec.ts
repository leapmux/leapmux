import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from './droid-fixtures'
import { bashToolCall, readToolCall, writeToolCall } from './helpers/providerToolCalls'
import { messageContents, sendMessage, waitForAgentIdle } from './helpers/ui'

/**
 * 259 — Factory Droid tool execution.
 *
 * Each call is Droid's own tool, which the mock scripts and Droid's own executor
 * runs in the agent's working directory. So each row draws the result that Droid
 * itself reports. The agent runs in Auto (High), so no banner stops a call; 260
 * covers the banners.
 */
droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.DROID

/** The visible chat text, joined. */
async function chatText(page: Parameters<typeof messageContents>[0]): Promise<string> {
  return (await messageContents(page).allTextContents()).join(' ')
}

droidTest.describe('Factory Droid tool execution', () => {
  droidTest('draws the output of a command', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    // The command text states no `droid-42`, so only the command's own output can
    // put it on the page.
    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'echo-call', 'echo "droid-$((40 + 2))"')] },
      { text: 'The command printed its number.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect.poll(() => chatText(page)).toContain('droid-42')
    // The executor ran the call: its result reached the next model call.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(followUp?.body)).toContain('droid-42')
  })

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
    await waitForAgentIdle(page, 180_000)

    await expect.poll(() => chatText(page)).toContain('droid-write-1')
    // The file really landed in the agent's working directory.
    expect(await import('node:fs').then(fs => fs.readFileSync(notes, 'utf8'))).toContain('droid-write-1')
  })
})

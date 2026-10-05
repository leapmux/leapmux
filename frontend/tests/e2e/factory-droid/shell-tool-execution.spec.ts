import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from '../droid-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

droidTest.describe('Factory Droid tool execution', () => {
  droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

  const PROVIDER = AgentProvider.DROID

  async function chatText(page: Parameters<typeof messageContents>[0]): Promise<string> {
    return (await messageContents(page).allTextContents()).join(' ')
  }

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
    await waitForAgentIdle(page)

    await expect.poll(() => chatText(page)).toContain('droid-42')
    // The executor ran the call: its result reached the next model call.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(followUp?.body)).toContain('droid-42')
  })
})

droidTest('runs successful and failed native commands with their actual output', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await exerciseShellToolExecution(context)
})

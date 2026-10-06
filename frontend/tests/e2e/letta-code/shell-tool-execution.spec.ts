import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest.describe('Letta Code tool execution', () => {
  const PROVIDER = AgentProvider.LETTA

  async function chatText(page: Parameters<typeof messageContents>[0]): Promise<string> {
    return (await messageContents(page).allTextContents()).join(' ')
  }

  lettaTest('draws the output of a command', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    // The command text states no `letta-42`, so only the command's own output can
    // put it on the page.
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'echo-call', 'echo "letta-$((40 + 2))"')] },
      { text: 'The command printed its number.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect.poll(() => chatText(page)).toContain('letta-42')
    // The executor ran the call: its result reached the next model call.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(followUp?.body)).toContain('letta-42')
  })
})

lettaTest('runs successful and failed native commands with their actual output', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseShellToolExecution(context)
})

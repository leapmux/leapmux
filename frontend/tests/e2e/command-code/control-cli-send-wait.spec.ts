import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, userBubbles } from '../helpers/ui'

function sendAndWait(agentId: string, prompt: string, requestId: string): string[] {
  return [
    'agent',
    'send',
    agentId,
    prompt,
    '--request-id',
    requestId,
    '--wait',
    '--timeout',
    '20s',
  ]
}

commandCodeTest('reads the original reply while a later native turn is still working', async ({ native, leapmuxServer, modelScript, page }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const prompt = 'Summarize the first readiness check.'
  const answer = 'The first readiness check completed.'
  await modelScript.queue({ text: answer })
  const first = sendAndWait(agentId, modelScript.prompt(prompt), 'command-code-readiness')
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [answer],
  })

  const nextAnswer = 'The native command produced the expected output.'
  const gate = 'command-code-finish-tool-turn'
  await modelScript.queue({
    toolCalls: [
      bashToolCall(native.provider, 'command-code-cli-output', 'printf command-code-output'),
    ],
  }, { text: nextAnswer, gate })
  const second = sendAndWait(agentId, modelScript.prompt('Run the scripted readiness command.'), 'command-code-tool')
  const retryWhileWorking = async () => {
    try {
      await modelScript.waitForGate(gate)
      expect(await runCLI(cli, first)).toMatchObject({
        input_id: completed.input_id,
        turn_outcome: 'completed',
        replies: [answer],
      })
    }
    finally {
      await modelScript.releaseGateIfHeld(gate)
    }
  }
  const [next] = await Promise.all([
    runCLI(cli, second),
    retryWhileWorking(),
  ])
  expect(next).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [nextAnswer],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: nextAnswer })).toHaveCount(1)
})

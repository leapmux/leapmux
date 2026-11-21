import { expect } from '@playwright/test'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { chooseQuestionOption } from '../helpers/nativeQuestion'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

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

mimoTest('returns the original exact reply while a later native question is open', async ({ native, page, leapmuxServer, modelScript }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const prompt = 'Summarize the first readiness check.'
  const answer = 'The first readiness check completed.'
  await modelScript.queue(nativeTextStep(native, answer))
  const first = sendAndWait(agentId, modelScript.prompt(prompt), 'mimo-readiness')
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [answer],
  })
  await waitForAgentIdle(page)

  const nextPrompt = 'Ask me which checklist to use.'
  const nextAnswer = 'The second checklist is selected.'
  const step = await modelScript.queue({
    toolCalls: [
      askUserQuestionToolCall(native.provider, 'mimo-cli-checklist', [
        {
          question: 'Which checklist should I use?',
          header: 'Checklist',
          options: [
            { label: 'Alpha', description: 'Use the first checklist.' },
            { label: 'Beta', description: 'Use the second checklist.' },
          ],
        },
      ]),
    ],
  }, nativeTextStep(native, nextAnswer))
  const second = sendAndWait(agentId, modelScript.prompt(nextPrompt), 'mimo-checklist')
  const waiting = await runCLI(cli, second) as { input_id: string }
  expect(waiting).toMatchObject({ message: 'delivered', state: 'waiting_for_user', replies: [] })
  const banner = await waitForControlBanner(page)
  expect(await runCLI(cli, first)).toMatchObject({
    input_id: completed.input_id,
    turn_outcome: 'completed',
    replies: [answer],
  })

  await chooseQuestionOption('Beta')(banner)
  await modelScript.waitForSteps(step + 2)
  expect(await runCLI(cli, second)).toMatchObject({
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [nextAnswer],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: nextAnswer })).toHaveCount(1)
})

import { expect } from '@playwright/test'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { chooseQuestionOption } from '../helpers/nativeQuestion'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { userBubbles, waitForControlBanner } from '../helpers/ui'
import { piTest } from '../pi-fixtures'

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

piTest('returns the original reply while a later input waits for an answer', async ({ native, leapmuxServer, modelScript, page }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(native)
  const prompt = 'Summarize the initial readiness check.'
  const answer = 'The initial readiness check completed.'
  await modelScript.queue({ text: answer })
  const first = sendAndWait(agentId, modelScript.prompt(prompt), 'pi-readiness')
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [answer],
  })

  const nextAnswer = 'The native question was answered.'
  const question = askUserQuestionToolCall(native.provider, 'pi-cli-question', [
    {
      question: 'Which route should I use?',
      header: 'Route',
      options: [
        { label: 'First', description: 'Use the first route.' },
        { label: 'Second', description: 'Use the second route.' },
      ],
    },
  ])
  const step = await modelScript.queue({
    toolCalls: [question],
  }, { text: nextAnswer })
  const second = sendAndWait(agentId, modelScript.prompt('Ask the scripted route question.'), 'pi-question')
  const waiting = await runCLI(cli, second) as { input_id: string }
  expect(waiting).toMatchObject({ message: 'delivered', state: 'waiting_for_user', replies: [] })
  const banner = await waitForControlBanner(page)
  expect(await runCLI(cli, first)).toMatchObject({
    input_id: completed.input_id,
    turn_outcome: 'completed',
    replies: [answer],
  })
  await chooseQuestionOption('Second')(banner)
  await modelScript.waitForSteps(step + 2)
  expect(await runCLI(cli, second)).toMatchObject({
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [nextAnswer],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
})

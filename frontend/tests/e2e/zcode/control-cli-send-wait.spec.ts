import { expect } from '@playwright/test'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { NATIVE_CONTROL_QUESTION } from '../helpers/nativeQuestion'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('retries the completed reply while a later ZCode turn waits for an answer', async ({ native, leapmuxServer }) => {
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const question = 'How many release checklist items remain open?'
  const answer = 'The release checklist has three open items.'
  const firstStep = await native.modelScript.queue(nativeTextStep(native, answer))
  const send = [
    'agent',
    'send',
    agentId,
    native.modelScript.prompt(question),
    '--wait',
    '--request-id',
    'first-release-checklist-input',
  ]
  const first = await runCLI(cli, send) as { input_id: string }
  expect(first).toMatchObject({
    id: agentId,
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [answer],
  })
  await native.modelScript.waitForSteps(firstStep + 1)
  await expect(assistantBubbles(native.page).filter({ hasText: answer })).toHaveCount(1)
  await waitForAgentIdle(native.page)

  const laterAnswer = 'The native question answer was recorded.'
  const laterStep = await native.modelScript.queue(
    {
      toolCalls: [
        askUserQuestionToolCall(native.provider, 'cli-wait-later-question', [NATIVE_CONTROL_QUESTION]),
      ],
    },
    nativeTextStep(native, laterAnswer),
  )
  const later = await runCLI(cli, [
    'agent',
    'send',
    agentId,
    native.modelScript.prompt('Ask the scripted question.'),
    '--wait',
    '--request-id',
    'second-release-checklist-input',
  ]) as { input_id: string }
  expect(later).toMatchObject({ state: 'waiting_for_user', message: 'delivered', replies: [] })
  expect(later.input_id).not.toBe(first.input_id)
  const banner = await waitForControlBanner(native.page)
  await expect(banner).toContainText(NATIVE_CONTROL_QUESTION.question)

  expect(await runCLI(cli, send)).toEqual({ ...first, state: 'waiting_for_user' })
  await expect(userBubbles(native.page).filter({ hasText: question })).toHaveCount(1)
  await expect(assistantBubbles(native.page).filter({ hasText: answer })).toHaveCount(1)
  await expect(banner).toContainText(NATIVE_CONTROL_QUESTION.question)

  const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
  expect(requests).toHaveLength(1)
  const [request] = requests
  if (!request)
    throw new Error('The Worker returned no pending question.')
  await runCLI(cli, [
    'agent',
    'answer',
    agentId,
    request.id,
    '--option',
    '2',
  ])
  await native.modelScript.waitForSteps(laterStep + 2)
  await expect(assistantBubbles(native.page).filter({ hasText: laterAnswer })).toHaveCount(1)
})

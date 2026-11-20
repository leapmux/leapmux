import { expect } from '@playwright/test'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { NATIVE_CONTROL_QUESTION } from '../helpers/nativeQuestion'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest('prints the canonical Kimi reply seen in the transcript', async ({ native, leapmuxServer }) => {
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const question = 'How many release checklist items remain open?'
  const answer = 'The release checklist has three open items.'
  const step = await native.modelScript.queue(nativeTextStep(native, answer))
  const args = [
    'agent',
    'send',
    agentId,
    native.modelScript.prompt(question),
    '--wait',
    '--all',
    '--request-id',
    'kimi-release-checklist-input',
  ]
  const result = await runCLI(cli, args)
  expect(result).toMatchObject({
    id: agentId,
    message: 'delivered',
    replies: [answer],
  })
  await native.modelScript.waitForSteps(step + 1)
  await waitForAgentIdle(native.page)
  expect(await runCLI(cli, args)).toEqual(result)
  await expect(userBubbles(native.page).filter({ hasText: question })).toHaveCount(1)
  await expect(assistantBubbles(native.page).filter({ hasText: answer })).toHaveCount(1)
})

kimiTest('retries a completed Kimi input while a later turn waits for an answer', async ({ native, leapmuxServer }) => {
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const question = 'Report the release checklist result.'
  const answer = 'The release checklist passed.'
  const firstStep = await native.modelScript.queue(nativeTextStep(native, answer))
  const firstArgs = [
    'agent',
    'send',
    agentId,
    native.modelScript.prompt(question),
    '--wait',
    '--all',
    '--request-id',
    'kimi-first-checklist',
  ]
  const first = await runCLI(cli, firstArgs) as { input_id: string }
  expect(first).toMatchObject({
    message: 'delivered',
    replies: [answer],
  })
  await native.modelScript.waitForSteps(firstStep + 1)
  await waitForAgentIdle(native.page)

  const laterAnswer = 'The choice was recorded.'
  const laterStep = await native.modelScript.queue(
    {
      toolCalls: [
        askUserQuestionToolCall(native.provider, 'kimi-cli-later-question', [NATIVE_CONTROL_QUESTION]),
      ],
    },
    nativeTextStep(native, laterAnswer),
  )
  const laterArgs = [
    'agent',
    'send',
    agentId,
    native.modelScript.prompt('Ask the scripted question.'),
    '--wait',
    '--request-id',
    'kimi-later-question',
  ]
  let replayed: unknown
  try {
    const later = await runCLI(cli, laterArgs, { timeoutMs: 15_000 })
    expect(later).toMatchObject({ message: 'delivered', state: 'waiting_for_user', replies: [] })
    const banner = await waitForControlBanner(native.page)
    await expect(banner).toContainText(NATIVE_CONTROL_QUESTION.question)
    replayed = await runCLI(cli, firstArgs)
    await expect(banner).toContainText(NATIVE_CONTROL_QUESTION.question)
  }
  finally {
    const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
    expect(requests).toHaveLength(1)
    await runCLI(cli, [
      'agent',
      'answer',
      agentId,
      requests[0]!.id,
      '--option',
      '2',
    ])
    await native.modelScript.waitForSteps(laterStep + 2)
    await expect(assistantBubbles(native.page).filter({ hasText: laterAnswer })).toHaveCount(1)
  }
  expect(replayed).toMatchObject({
    input_id: first.input_id,
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [answer],
  })
  expect(await runCLI(cli, laterArgs)).toMatchObject({
    turn_outcome: 'completed',
    replies: [laterAnswer],
  })
  await expect(userBubbles(native.page).filter({ hasText: question })).toHaveCount(1)
})

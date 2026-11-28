import type { CLIConfigDir } from '../helpers/cli'
import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { NATIVE_CONTROL_QUESTION } from '../helpers/nativeQuestion'
import { currentNativeAgent, nativeScenarioModelContextText, nativeTextStep } from '../helpers/nativeScenario'
import { askUserQuestionToolCall } from '../helpers/providerToolCalls'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { assistantBubbles, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

async function answerZCodeQuestion(cli: CLIConfigDir, agentId: string): Promise<void> {
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
}

zcodeTest('returns the reply after the native turn consumes a queued steer', async ({ native, leapmuxServer }) => {
  const { page, modelScript } = native
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const leadReply = 'The first checklist is ready.'
  const steerPrompt = 'Check the revised release checklist.'
  const steerReply = 'The revised checklist has two open items.'
  const gate = 'zcode-cli-steer-lead'
  const lead = [
    'agent',
    'send',
    agentId,
    modelScript.prompt('Check the first release checklist.'),
    '--request-id',
    'zcode-cli-lead',
  ]
  const steer = [
    'agent',
    'send',
    agentId,
    modelScript.prompt(steerPrompt),
    '--request-id',
    'zcode-cli-steer',
  ]

  await withCleanup(async () => {
    const start = await modelScript.queue(
      {
        ...nativeTextStep(native, leadReply),
        stream: {
          chunkChars: 10,
          delayMs: 0,
          gates: [
            { afterChunk: 1, name: gate },
          ],
        },
      },
      {
        toolCalls: [
          askUserQuestionToolCall(native.provider, 'cli-wait-steered-question', [NATIVE_CONTROL_QUESTION]),
        ],
      },
      nativeTextStep(native, steerReply),
    )
    await runCLI(cli, lead)
    await modelScript.waitForGate(gate)
    const queued = await runCLI(cli, steer) as { input_id: string }
    const row = queuedInputRow(page, steerPrompt)
    await steerButton(row).click()
    await expectSteerAccepted(page, row)
    await modelScript.releaseGate(gate)
    await modelScript.waitForSteps(start + 2)
    expect(nativeScenarioModelContextText(native, await modelScript.requestAt(start + 1))).toContain(steerPrompt)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText(NATIVE_CONTROL_QUESTION.question)
    expect(await runCLI(cli, [...steer, '--wait', '--all'])).toMatchObject({
      input_id: queued.input_id,
      state: 'waiting_for_user',
      message: 'delivered',
      replies: [],
    })
    await answerZCodeQuestion(cli, agentId)
    await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)

    await expect(userBubbles(page).filter({ hasText: steerPrompt })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: steerReply })).toHaveCount(1)
    expect(await runCLI(cli, [...steer, '--wait', '--all'])).toMatchObject({
      input_id: queued.input_id,
      message: 'delivered',
      turn_outcome: 'completed',
      replies: [steerReply],
    })
    expect(await runCLI(cli, [...lead, '--wait', '--all'])).toMatchObject({
      turn_outcome: 'completed',
      replies: [leadReply, steerReply],
    })
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})

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

  await answerZCodeQuestion(cli, agentId)
  await native.modelScript.waitForSteps(laterStep + 2)
  await expect(assistantBubbles(native.page).filter({ hasText: laterAnswer })).toHaveCount(1)
})

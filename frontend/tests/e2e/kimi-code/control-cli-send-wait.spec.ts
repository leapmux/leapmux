import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { NATIVE_CONTROL_QUESTION } from '../helpers/nativeQuestion'
import { currentNativeAgent, nativeModelContextText, nativeTextStep } from '../helpers/nativeScenario'
import { askUserQuestionToolCall, bashToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectSteerAccepted, queuedInputRow, steerButton } from '../helpers/steer'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'
import { kimiChildTurn, prepareKimiChildRun } from './childScenario'

function sendAndWait(agentId: string, text: string, key: string): string[] {
  return [
    'agent',
    'send',
    agentId,
    text,
    '--request-id',
    key,
    '--wait',
    '--all',
    '--timeout',
    '25s',
  ]
}

kimiTest('binds a queued steer to the turn that reads it', async ({ native, leapmuxServer, page, modelScript }) => {
  const { id: agentId } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const gate = 'kimi-cli-steer-original'
  const prompt = 'Use the revised checklist.'
  const text = modelScript.prompt(prompt)
  const answer = 'The revised checklist was verified.'
  const key = 'kimi-cli-steer-revision'
  const initial = sendAndWait(agentId, modelScript.prompt('Check the original list.'), 'kimi-cli-steer-original')
  const steer = sendAndWait(agentId, text, key)
  const start = await modelScript.queue({
    gate,
    toolCalls: [
      bashToolCall(native.provider, 'kimi-cli-held-tool', 'printf original-checklist'),
    ],
  }, {
    toolCalls: [
      askUserQuestionToolCall(native.provider, 'kimi-cli-steered-question', [NATIVE_CONTROL_QUESTION]),
    ],
  }, nativeTextStep(native, answer))

  const [original] = await withCleanup(async () => await Promise.all([
    runCLI(cli, initial) as Promise<{ input_id: string }>,
    (async () => {
      await modelScript.waitForGate(gate)
      await runCLI(cli, [
        'agent',
        'send',
        agentId,
        text,
        '--request-id',
        key,
      ])
      const row = queuedInputRow(page, prompt)
      await steerButton(row).click()
      await expectSteerAccepted(page, row)
      await modelScript.releaseGateIfHeld(gate)
    })(),
  ]), async () => { await modelScript.releaseGateIfHeld(gate) })
  expect(original).toMatchObject({ turn_outcome: 'completed', replies: [] })
  expect(nativeModelContextText(await modelScript.requestAt(start + 1))).toContain(prompt)

  const waiting = await runCLI(cli, steer) as { input_id: string }
  expect(waiting).toMatchObject({ state: 'waiting_for_user', replies: [] })
  await expect(await waitForControlBanner(page)).toContainText(NATIVE_CONTROL_QUESTION.question)
  expect(await runCLI(cli, initial)).toMatchObject({
    input_id: original.input_id,
    turn_outcome: 'completed',
    replies: [],
  })
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
  expect(await runCLI(cli, steer)).toMatchObject({
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [answer],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
})

kimiTest('retrieves each identical child prompt by its native receipt', async ({ native, leapmuxServer, page, modelScript }) => {
  await prepareKimiChildRun(page)
  const task = 'Prepare the initial child checklist.'
  await modelScript.rule({
    name: 'the native child completes its initial task',
    when: kimiChildTurn(task),
    respond: { text: 'The child is ready for the checklist.' },
    once: true,
  })
  const start = await modelScript.queue({
    toolCalls: [
      spawnSubagentToolCall(native.provider, 'kimi-cli-exact-child', {
        description: 'Prepare the checklist',
        prompt: modelScript.prompt(task),
      }),
    ],
  }, { text: 'The child prepared the checklist.' })
  await sendMessage(page, modelScript.prompt('Start a child to prepare the checklist.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  const row = await requireRegistryRow(page)
  await expectRowBecomesFinal(page, row)
  const childId = await openChildTabFromRow(page, row)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const prompt = 'Verify the child checklist.'
  const text = modelScript.prompt(prompt)
  const firstReply = 'The child verified the first checklist.'
  const secondReply = 'The child verified the second checklist.'
  const first = sendAndWait(childId, text, 'kimi-cli-child-first')
  const second = sendAndWait(childId, text, 'kimi-cli-child-second')
  await modelScript.rule({
    name: 'the child answers the first keyed prompt',
    when: kimiChildTurn(prompt),
    respond: { text: firstReply },
    once: true,
  })
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    id: childId,
    turn_outcome: 'completed',
    replies: [firstReply],
  })

  const gate = 'kimi-cli-child-second-reply'
  await modelScript.rule({
    name: 'the child holds its second keyed reply',
    when: kimiChildTurn(prompt),
    respond: { text: secondReply, gate },
    once: true,
  })
  const [next] = await withCleanup(async () => await Promise.all([
    runCLI(cli, second) as Promise<{ input_id: string }>,
    (async () => {
      await modelScript.waitForGate(gate)
      expect(await runCLI(cli, first)).toMatchObject({
        input_id: completed.input_id,
        turn_outcome: 'completed',
        replies: [firstReply],
      })
      await modelScript.releaseGate(gate)
    })(),
  ]), async () => { await modelScript.releaseGateIfHeld(gate) })
  expect(next).toMatchObject({
    id: childId,
    turn_outcome: 'completed',
    replies: [secondReply],
  })
  expect(next.input_id).not.toBe(completed.input_id)
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(2)
})

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
